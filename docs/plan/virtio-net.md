# Plan: virtio-net

Created: 2026-03-31T19:00:00+08:00
Status: PAUSED
Source: [plan/virtio-devices#Phase3-4]

## Task Description

Implement virtio-net networking on macOS via vmnet.framework. Guest gets DHCP IP, NAT internet access. End-to-end: `ping 8.8.8.8` and `apk add curl` from the Alpine guest.

## Alternatives & Trade-offs

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| vmnet.framework direct | Native macOS, DHCP/NAT built-in, L2 Ethernet | Requires root for shared mode | **SELECTED** |
| socket_vmnet daemon | No root for main process | Extra process, deployment complexity | Deferred |
| gvisor-tap-vsock | No root, pure Go | Foreign code, slower | Rejected |

## Phases

### Phase 1: Kernel rebuild with CONFIG_NET + CONFIG_INET

**Objective**: Rebuild kernel with networking support. Currently `CONFIG_NET is not set`.

**Expected Results**:
- [ ] `CONFIG_NET=y`, `CONFIG_INET=y` added to defconfig
- [ ] Kernel rebuilt successfully
- [ ] Boot test: `socket(AF_INET,...)` no longer returns "Function not implemented"

**Dependencies**: None
**Status**: COMPLETE

### Phase 2: VmnetTap — vmnet.framework backend implementing TapT

**Objective**: Create `net_util/src/sys/macos/vmnet_tap.rs` implementing the `TapT` trait backed by vmnet.framework shared mode.

**Expected Results**:
- [ ] `VmnetTap` struct with vmnet FFI: `vmnet_start_interface`, `vmnet_stop_interface`, `vmnet_read`, `vmnet_write`
- [ ] Implements `Read` (vmnet_read → returns Ethernet frame), `Write` (vmnet_write), `AsRawDescriptor` (pipe fd for kqueue)
- [ ] Implements `TapTCommon` methods: `new`, `mac_address`, `mtu`, `set_ip_addr`, `enable`, `try_clone`
- [ ] Implements `ReadNotifier` returning pipe fd that becomes readable when vmnet has packets
- [ ] Event bridging: GCD callback → pipe write → kqueue detects readable
- [ ] Single-iovec coalescing for vmnet_write (vmnet limitation)
- [ ] `vmnet_stop_interface` called on Drop
- [ ] `cargo build --no-default-features` compiles

**Dependencies**: None (can develop independently)
**Risks**:
- vmnet requires root — tests must run with sudo
- GCD callback thread safety
- vmnet_write single-iovec constraint
**Status**: PENDING

### Phase 3: macOS net sys — process_tx/rx + device registration

**Objective**: Implement `devices/src/virtio/net/sys/macos.rs` with TX/RX processing, and register virtio-net device in `run_config`.

**Expected Results**:
- [ ] `process_tx`, `process_rx`, `process_mrg_rx` implemented (can adapt from Linux versions)
- [ ] `validate_and_configure_tap` and `virtio_features_to_tap_offload` implemented
- [ ] `run_config` creates Net device from VmnetTap, registers on PCI bus
- [ ] `cargo build --no-default-features` compiles

**Dependencies**: Phase 2 (VmnetTap)
**Status**: PENDING

### Phase 4: End-to-end networking test

**Objective**: Guest gets DHCP IP and internet access via vmnet NAT.

**Expected Results**:
- [ ] Boot with `--net vmnet` flag (or similar)
- [ ] Guest kernel detects `virtio_net virtio1` and creates `eth0`
- [ ] `udhcpc -i eth0` obtains DHCP lease (192.168.x.x from vmnet)
- [ ] `ping -c 3 8.8.8.8` succeeds (NAT internet)
- [ ] `ping -c 3 google.com` succeeds (DNS)
- [ ] `apk update && apk add curl` succeeds

**Dependencies**: Phase 1 (kernel), Phase 3 (device registration)
**Risks**: vmnet requires root to run crosvm
**Status**: PENDING

## Findings

### F-001: CONFIG_NET missing from kernel
`CONFIG_NET is not set` in the kernel .config despite `CONFIG_VIRTIO_NET=y` in defconfig. The defconfig only adds VIRTIO_NET but not the base CONFIG_NET which it depends on. Result: `socket()` returns ENOSYS.

### F-002: vmnet single-iovec write constraint
QEMU found that `vmnet_write()` only accepts a single iovec per packet descriptor. Multiple iovecs return `VMNET_INVALID_ARGUMENT`. TX path must coalesce scatter-gather before write.

### F-003: vmnet GCD callback threading
`vmnet_interface_set_event_callback()` fires on a GCD dispatch queue thread. Must bridge to crosvm's event loop via a pipe fd.
