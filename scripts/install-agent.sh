#!/bin/bash
# install-agent.sh — Install aetheria-agent into the VM rootfs image.
#
# Usage: ./scripts/install-agent.sh
#
# Requires Docker (to mount ext4 image on macOS).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
ROOTFS_IMG="$ROOT_DIR/aetheria-kernel/build/rootfs-arm64.img"
AGENT_BIN="/tmp/aetheria-agent"

echo "=== Install aetheria-agent into rootfs ==="

# Build agent if not present.
if [ ! -f "$AGENT_BIN" ]; then
    echo "Building agent..."
    cd "$ROOT_DIR"
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "$AGENT_BIN" ./cmd/aetheria-agent/
fi
echo "Agent: $(ls -lh "$AGENT_BIN" | awk '{print $5}')"

# Install into rootfs via Docker (ext4 mount requires Linux).
echo "Installing into rootfs..."
docker run --rm --privileged --platform linux/arm64 \
    -v "$(dirname "$ROOTFS_IMG"):/work" \
    -v "$AGENT_BIN:/agent:ro" \
    arm64v8/alpine:3.21 sh -c "
set -e
mount -o loop /work/$(basename "$ROOTFS_IMG") /mnt

# Install agent binary
cp /agent /mnt/usr/bin/aetheria-agent
chmod +x /mnt/usr/bin/aetheria-agent

# Create OpenRC service for auto-start
cat > /mnt/etc/init.d/aetheria-agent << 'SVC'
#!/sbin/openrc-run

name=\"aetheria-agent\"
description=\"Aetheria guest agent (vsock)\"
command=\"/usr/bin/aetheria-agent\"
command_background=true
pidfile=\"/run/aetheria-agent.pid\"
output_log=\"/var/log/aetheria-agent.log\"
error_log=\"/var/log/aetheria-agent.log\"

depend() {
    after net
}
SVC
chmod +x /mnt/etc/init.d/aetheria-agent

# Enable the service at default runlevel
mkdir -p /mnt/etc/runlevels/default
ln -sf /etc/init.d/aetheria-agent /mnt/etc/runlevels/default/aetheria-agent 2>/dev/null || true

# NOTE: Do NOT add to inittab — OpenRC service is sufficient.
# Having both causes two agent instances that fight over the vsock connection.

# Create directories agent needs
mkdir -p /mnt/var/aetheria/containers /mnt/var/aetheria/images /mnt/var/log

# Install tools needed for container operations and 3D graphics
# util-linux: nsenter; iproute2: ip; nftables: NAT; mesa: GPU drivers
chroot /mnt /bin/sh -c 'apk add --no-cache \
    util-linux tar iproute2 iptables nftables \
    mesa-dri-gallium mesa-gl mesa-egl mesa-gles mesa-gbm mesa-utils' 2>&1 || true

# Enable getty on tty0 (framebuffer console) so fbcon stays active.
# Without this, fb0 shows nothing because no process writes to tty0.
if ! grep -q 'tty0' /mnt/etc/inittab 2>/dev/null; then
    echo 'tty0::respawn:/sbin/getty 38400 tty0' >> /mnt/etc/inittab
fi

sync
umount /mnt
echo 'Agent installed successfully.'
"

echo ""
echo "=== Done ==="
echo "Agent installed in $ROOTFS_IMG"
echo "VM will auto-start agent on boot via OpenRC + inittab respawn."
