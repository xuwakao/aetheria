# Progress: virtio-vsock

Created: 2026-04-01
Source: [plan/virtio-vsock]

## Log

### [2026-04-01T04:15] META-PHASE A — Planning
Created 4-phase plan for userspace virtio-vsock on macOS.
Reference implementation: Windows crosvm vsock (~1600 LOC).
Key decision: Use Unix domain sockets as host-side transport (replacing Windows named pipes).

### [2026-04-01T04:15] META-PHASE B — Plan Review

| # | Item | Verdict | Evidence |
|---|------|---------|----------|
| 1 | Dependency validation | PASS | Phase 1 independent; 2→1; 3→2; 4→3. Linear chain, no cycles. |
| 2 | Expected results precision | PASS | Each phase has compile/boot/runtime checks. Phase 4 has concrete commands. |
| 3 | Feasibility — Phase 1 | PASS | VirtioDevice trait well-understood from virtio-net work. Stub exists. |
| 4 | Feasibility — Phase 2 | RISK | cros_async executor on macOS untested for vsock worker pattern. May need poll fallback. |
| 5 | Feasibility — Phase 3 | RISK | Unix socket path convention needs design. Credit flow control complex. |
| 6 | Feasibility — Phase 4 | PASS | Standard vsock tools (socat, ncat) available in Alpine. |
| 7 | Stub vs real | PASS | All phases produce real implementations. No stubs planned. |

### [2026-04-01T05:20] Phase 1-3 — Implementation

Implemented full userspace vsock device in `devices/src/virtio/vsock/sys/macos.rs` (674 LOC):
- VirtioDevice trait: config space (guest_cid), 3 queues (RX/TX/event)
- TX: OP_REQUEST→UnixStream::connect, OP_RW→stream.write, OP_SHUTDOWN/RST→cleanup
- RX: poll host sockets every 10ms, forward data via OP_RW to guest RX queue
- Credit flow control: buf_alloc/fwd_cnt tracking, threshold-based updates
- Device registered at PCI 00:03.0 with CID=3

Also fixed: CONFIG_VSOCKETS=y missing (parent toggle for VIRTIO_VSOCKETS).

### Review: Phase 1-3

| # | Expected Result | Actual Result | Evidence | Verdict |
|---|-----------------|---------------|----------|---------|
| 1 | PCI device enumerated | `[1af4:1053]` at 00:03.0 | Boot log | PASS |
| 2 | Kernel PF_VSOCK registered | `NET: Registered PF_VSOCK protocol family` | Boot log | PASS |
| 3 | /dev/vsock exists | `crw------- root root 10, 126` | Test script | PASS |
| 4 | Worker thread running | `vsock: worker started, cid=3` | Device log | PASS |
| 5 | Guest→Host connection | `OP_REQUEST → connected host:5555<->guest:*` | Device log | PASS |

**Overall Verdict**: PASS

### [2026-04-01T05:25] Phase 4 — Integration Test

Guest socat connected to host Python Unix socket listener via vsock CID=2 port=5555.
Connection established, shutdown handled correctly.

**Findings this phase**: 2

## Plan Corrections

## Findings

### F-001: CONFIG_VSOCKETS parent toggle
Same pattern as CONFIG_NETDEVICES: `CONFIG_VIRTIO_VSOCKETS=y` requires `CONFIG_VSOCKETS=y` as parent.
Without it, the kernel silently ignores the virtio-vsock driver config.

### F-002: Guest sends OP_RST after OP_SHUTDOWN
The Linux virtio_transport guest driver sends both OP_SHUTDOWN and OP_RST when closing a connection.
The initial implementation only handled OP_SHUTDOWN, causing "unknown op 3" warnings.
Fixed by routing OP_RST through the same shutdown handler.
