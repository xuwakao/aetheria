#!/bin/bash
# run.sh — One-click Aetheria launcher
#
# Usage:
#   ./run.sh              Start the VM daemon
#   ./run.sh create alpine  Create and start a container
#   ./run.sh shell alpine   Interactive shell in container
#   ./run.sh ls             List containers
#   ./run.sh exec <cmd>     Execute command in VM
#   ./run.sh stop           Shutdown VM
#   ./run.sh build          Rebuild agent + CLI + install into rootfs

set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"

export AETHERIA_CROSVM="$ROOT/aetheria-crosvm/target/release/crosvm"
export AETHERIA_KERNEL="$ROOT/aetheria-kernel/build/linux-6.12.15/arch/arm64/boot/Image"
export AETHERIA_ROOTFS="$ROOT/aetheria-kernel/build/rootfs-arm64.img"
export AETHERIA_SHARE="/private/tmp/aetheria-share"

# Data disk: dedicated sparse ext4 for container images and overlayfs.
# Thin provisioning: 64GB virtual size, actual host usage grows on demand.
AETHERIA_DATA="$ROOT/.data/aetheria-data.img"
AETHERIA_DATA_SIZE="64G"

CLI="$ROOT/.build/aetheria-cli"
AGENT="$ROOT/.build/aetheria-agent"

mkdir -p "$ROOT/.build" "$AETHERIA_SHARE"

# ── Build if needed ──
build() {
    echo "Building agent (ARM64 Linux)..."
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "$AGENT" ./cmd/aetheria-agent/

    echo "Building CLI (macOS)..."
    go build -o "$CLI" ./cmd/aetheria/

    echo "Installing agent into rootfs (needs Docker)..."
    docker run --rm --privileged --platform linux/arm64 \
        -v "$ROOT/aetheria-kernel/build:/work" \
        -v "$AGENT:/agent:ro" \
        arm64v8/alpine:3.21 sh -c \
        "mount -o loop /work/rootfs-arm64.img /mnt && cp /agent /mnt/usr/bin/aetheria-agent && chmod +x /mnt/usr/bin/aetheria-agent && sync && umount /mnt"

    echo "Build complete."
}

# Build CLI if not present
if [ ! -f "$CLI" ]; then
    echo "First run — building..."
    build
fi

# ── Data disk (thin provisioned) ──
ensure_data_disk() {
    if [ -f "$AETHERIA_DATA" ]; then
        return
    fi
    echo "Creating data disk ($AETHERIA_DATA_SIZE thin provisioned)..."
    mkdir -p "$(dirname "$AETHERIA_DATA")"
    # Create sparse file — only uses actual host disk for written blocks.
    dd if=/dev/zero of="$AETHERIA_DATA" bs=1 count=0 seek=$AETHERIA_DATA_SIZE 2>/dev/null
    # Format as ext4 inside Docker (mkfs.ext4 not on macOS)
    docker run --rm -v "$(dirname "$AETHERIA_DATA"):/work" alpine:3.21 \
        sh -c "apk add --no-cache e2fsprogs >/dev/null 2>&1 && mkfs.ext4 -F -L aetheria-data /work/$(basename "$AETHERIA_DATA")" 2>&1
    echo "Data disk ready: $AETHERIA_DATA"
    echo "  Virtual size: $AETHERIA_DATA_SIZE"
    echo "  Actual usage: $(du -h "$AETHERIA_DATA" | cut -f1)"
}

# ── Signing ──
ensure_signed() {
    if ! codesign -v "$AETHERIA_CROSVM" 2>/dev/null; then
        echo "Signing crosvm with Hypervisor entitlement..."
        cat > /tmp/aetheria-ent.plist << 'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>com.apple.security.hypervisor</key><true/>
</dict></plist>
PLIST
        codesign --sign - --entitlements /tmp/aetheria-ent.plist --force "$AETHERIA_CROSVM"
    fi
}

# ── Commands ──
case "${1:-run}" in
    build)
        build
        ;;
    run)
        ensure_signed
        ensure_data_disk
        echo ""
        echo "  ╔══════════════════════════════════════╗"
        echo "  ║     Aetheria — 以太之境              ║"
        echo "  ║     Starting VM...                   ║"
        echo "  ╚══════════════════════════════════════╝"
        echo ""
        echo "  In another terminal, run:"
        echo "    ./run.sh create alpine"
        echo "    ./run.sh shell alpine"
        echo ""
        # crosvm needs sudo for vmnet (network)
        sudo -v 2>/dev/null || echo "Enter password for VM networking (vmnet):"
        export AETHERIA_DATA_DISK="$AETHERIA_DATA"
        exec "$CLI" run
        ;;
    create|start|shell|ls|exec|stop|ping|info|rm|pull|images)
        exec "$CLI" "$@"
        ;;
    *)
        echo "Usage: ./run.sh [command]"
        echo ""
        echo "  run              Start VM daemon (default)"
        echo "  build            Rebuild agent + CLI"
        echo "  create <distro>  Create container (alpine, ubuntu)"
        echo "  shell <name>     Interactive shell"
        echo "  ls               List containers"
        echo "  exec <cmd>       Execute in VM"
        echo "  stop             Shutdown VM"
        echo "  ping             Health check"
        ;;
esac
