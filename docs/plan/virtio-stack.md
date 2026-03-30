# Plan: virtio-stack

Created: 2026-03-31T17:00:00+08:00
Status: DEPRECATED
Superseded-by: [plan/virtio-devices]
Source: Full macOS stub audit + vmnet.framework research

## Task Description

Implement the complete virtio device stack on macOS to transform Aetheria from a boot-to-shell demo into a functional container runtime. Four major subsystems: block storage, file sharing, networking, and host-guest communication.

## Alternatives & Trade-offs

### Networking Backend

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| vmnet.framework (direct) | Native macOS, DHCP/NAT built-in, L2 Ethernet | Requires root or restricted entitlement | **SELECTED** for initial implementation |
| socket_vmnet (daemon) | No root for main process, Lima/Colima proven | Extra daemon process, deployment complexity | Deferred — optimization for distribution |
| gvisor-tap-vsock | Pure userspace, no root, cross-platform | Go dependency, slower, complex integration | Rejected — too much foreign code |
| utun | No root, simple | L3 only, no Ethernet/DHCP/broadcast, point-to-point | Rejected — insufficient for VM networking |

### File Sharing

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| virtio-9p | Already implemented in crosvm, just cfg-gated | Slower than virtio-fs, no mmap | **SELECTED** as Phase 2 quick win |
| virtio-fs (FUSE) | Better performance, mmap support | FUSE server complexity, platform concerns | Deferred to later plan |
| vhost-user-fs | Best performance, OrbStack approach | All vhost-user infra is stubbed, massive effort | Deferred |
| reverse-sshfs | No kernel changes needed | Very slow, Lima's legacy approach | Rejected |

### Rootfs Strategy

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| Alpine minirootfs on virtio-blk | Small (8MB), apk, fast boot, standard ext4 | Needs disk image management | **SELECTED** |
| Debian via debootstrap | Full package ecosystem | Large (300MB+), slow boot | Deferred |
| erofs/squashfs + overlayfs | Read-only base, CoW writes, OrbStack-like | Complex image pipeline | Deferred — production optimization |

### Vsock Implementation

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| Userspace virtio-vsock device | Full control, no kernel dependency | Must implement CID routing, connection state machine | **SELECTED** |
| Unix socket bridge | Simpler bridging | Limited functionality | Rejected — doesn't implement vsock protocol |

## Phases

### Phase 1: virtio-blk + Alpine rootfs

**Objective**: Boot a complete Alpine Linux userspace from a virtio-blk ext4 disk image, replacing the busybox initramfs.

**Expected Results**:
- [ ] `run_config` accepts `--disk` option and creates BlockAsync device
- [ ] BlockAsync device registered on PCI bus with GIC interrupt
- [ ] Build script downloads Alpine minirootfs and creates ext4 image
- [ ] Kernel boots from initramfs, pivots root to virtio-blk /dev/vda
- [ ] Alpine login prompt visible on serial console
- [ ] `apk update` works (after Phase 3 networking — initially verify mount + shell)
- [ ] `cargo build --no-default-features` compiles

**Dependencies**: None
**Risks**: virtio-blk PCI enumeration may fail if device registration order is wrong. Mitigation: verify PCI BAR assignment and interrupt mapping in FDT.
**Status**: PENDING

### Phase 2: virtio-9p file sharing

**Objective**: Share a host directory with the guest via virtio-9p, enabling host-guest file exchange.

**Expected Results**:
- [ ] Ungating: `devices/src/virtio/p9.rs` compiles on macOS (remove Linux-only cfg_if guard)
- [ ] `run_config` accepts `--shared-dir` option and creates P9 device
- [ ] P9 device registered on PCI bus
- [ ] Guest mounts shared directory: `mount -t 9p -o trans=virtio share0 /mnt`
- [ ] File created on host visible in guest and vice versa
- [ ] `cargo build --no-default-features` compiles

**Dependencies**: Phase 1 (need working virtio-PCI path proven by blk)
**Risks**: p9 crate may have Linux-specific I/O paths. Mitigation: audit p9 crate source for platform assumptions.
**Status**: PENDING

### Phase 3: virtio-net + vmnet.framework

**Objective**: Provide guest networking via vmnet.framework shared mode (NAT), enabling internet access from the VM.

**Expected Results**:
- [ ] `net_util/src/sys/macos.rs` implements TapT trait backed by vmnet.framework
- [ ] vmnet FFI bindings in a new `vmnet_sys` module or inline in net_util
- [ ] vmnet interface created in shared mode with DHCP
- [ ] virtio-net device created with vmnet backend, registered on PCI bus
- [ ] Guest kernel detects `virtio_net` device, gets DHCP IP address
- [ ] `ping 8.8.8.8` works from guest
- [ ] `apk update && apk add curl` works in Alpine guest
- [ ] `cargo build --no-default-features` compiles
- [ ] Entitlement `com.apple.vm.networking` NOT required (shared mode + root)

**Dependencies**: Phase 1 (Alpine rootfs for testing)
**Risks**:
- vmnet requires root — initial implementation will run as root; unprivileged mode (socket_vmnet) deferred.
- vmnet event callback runs on GCD thread — must bridge to crosvm's kqueue reactor safely.
- QEMU found that vmnet_write only supports single-iovec — must coalesce before write.

**Status**: PENDING

### Phase 4: virtio-vsock

**Objective**: Implement virtio-vsock for efficient host-guest communication, enabling future agent/control-plane integration.

**Expected Results**:
- [ ] `devices/src/virtio/vsock/sys/macos.rs` implements Vsock device with userspace transport
- [ ] Host-side listener: Unix domain socket mapped to vsock CID+port
- [ ] Guest-side: `socat - VSOCK-CONNECT:2:1234` connects to host listener
- [ ] Bidirectional data transfer works
- [ ] CID 2 (host) properly routed
- [ ] `cargo build --no-default-features` compiles

**Dependencies**: Phase 1 (need guest with socat or netcat for testing)
**Risks**: vsock connection state machine is complex (LISTEN, REQUEST, ESTABLISHED, CLOSING, etc.). Mitigation: implement minimal subset first (connect + stream only, no credit-based flow control initially).
**Status**: PENDING

### Phase 5: Integration test + initramfs update

**Objective**: Validate all components working together, update initramfs as a minimal bootstrap that pivots to disk.

**Expected Results**:
- [ ] Boot sequence: initramfs → detect /dev/vda → mount ext4 → switch_root
- [ ] After pivot: Alpine with networking, 9p mount, vsock
- [ ] End-to-end test: boot → get IP → install package → write file to shared dir → vsock echo
- [ ] Clean shutdown via `poweroff` (PSCI SYSTEM_OFF path)
- [ ] All documentation updated

**Dependencies**: Phases 1-4
**Status**: PENDING

## Findings

### F-001: Virtio-PCI transport already functional on macOS
PCI bus enumeration, BAR assignment, and GIC interrupt routing work on macOS. All virtio devices are registered as PCI devices via `generate_pci_root()` in `aarch64/src/lib.rs:754-777`. The FDT includes proper PCI host bridge and interrupt-map nodes. No macOS-specific changes needed for the transport layer.

### F-002: virtio-blk macOS backend already implemented
`devices/src/virtio/block/sys/macos.rs` has a complete implementation: `get_seg_max()`, `DiskOption::open()`, and `BlockAsync::create_executor()` with kqueue-based async executor. The device just needs to be created and registered in `run_config`.

### F-003: virtio-9p is platform-agnostic but Linux-gated
`devices/src/virtio/p9.rs` is a complete 9P-over-virtio implementation using the `p9` crate. It's excluded from macOS only by a `cfg_if` guard in `devices/src/virtio/mod.rs`. The p9 crate itself uses standard Rust I/O (no Linux-specific syscalls) [UNVERIFIED — needs p9 crate source audit].

### F-004: vmnet.framework requires root for shared mode
Shared mode (NAT) works without the restricted `com.apple.vm.networking` entitlement but requires running as root. The socket_vmnet approach (privileged daemon + unprivileged client) is the production solution but adds deployment complexity. Initial implementation will use direct vmnet with root.

### F-005: vmnet single-iovec limitation
QEMU's implementation discovered that `vmnet_write()` only accepts a single iovec per packet descriptor. Multiple iovecs return `VMNET_INVALID_ARGUMENT`. TX path must coalesce scatter-gather buffers before writing.

### F-006: vmnet event callback runs on GCD thread
`vmnet_interface_set_event_callback()` fires on a GCD dispatch queue thread, not the crosvm event loop thread. Must use a pipe or eventfd to bridge the notification into the kqueue reactor's wait loop.
