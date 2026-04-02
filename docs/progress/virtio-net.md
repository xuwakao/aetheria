# Progress: virtio-net

Created: 2026-03-31T19:00:00+08:00
Source: [plan/virtio-net]

## Log

### [2026-03-31T19:00] META-PHASE A — Planning
Created 4-phase plan for vmnet.framework networking.
Key finding: CONFIG_NET missing from kernel — must add before any networking can work.

### [2026-03-31T19:00] META-PHASE B — Plan Review

| # | Item | Verdict | Evidence |
|---|------|---------|----------|
| 1 | Dependency validation | PASS | Phase 1 independent; Phase 2 independent; Phase 3 depends on 2; Phase 4 depends on 1+3. No cycles. |
| 2 | Expected results precision | PASS | Each phase has compile check + runtime verification. Phase 4 has concrete commands (ping, apk). |
| 3 | Feasibility — Phase 1 | PASS | Simple defconfig change + kernel rebuild. |
| 4 | Feasibility — Phase 2 | RISK | vmnet FFI is new code. QEMU's implementation is reference. Requires root. |
| 5 | Feasibility — Phase 3 | PASS | Can adapt Linux process_tx/rx with minimal changes. |
| 6 | Feasibility — Phase 4 | RISK | Requires sudo to run. DHCP from vmnet depends on correct L2 framing. |
| 7 | Stub vs real | PASS | All phases produce real implementations. |

### [2026-03-31T19:05] Starting Phase 1 — Kernel rebuild with CONFIG_NET
**Expected**: CONFIG_NET=y + CONFIG_INET=y in defconfig, kernel builds, socket() works.

### Review: Phase 1

| # | Expected Result | Actual Result | Evidence | Verdict |
|---|-----------------|---------------|----------|---------|
| 1 | CONFIG_NET=y, CONFIG_INET=y in defconfig | Both added | `aetheria_arm64_defconfig` | PASS |
| 2 | Kernel rebuilds | Built, 17MB (up from 12MB with networking) | Build output | PASS |
| 3 | socket() no longer returns ENOSYS | `NET: Registered PF_INET` in kernel log, Alpine boots cleanly | Boot log | PASS |

**Overall Verdict**: PASS
**Findings this phase**: 1 (F-001 — CONFIG_NET was missing)

### [2026-03-31T19:55] Phase 1 Functional Acceptance
- Build: kernel builds OK
- Boot: Alpine login, PF_INET/PF_INET6 registered
- PASS

### [2026-03-31T19:55] Starting Phase 2 — VmnetTap implementation
**Expected**: VmnetTap struct with vmnet FFI, implements TapTCommon + Read + Write + AsRawDescriptor + ReadNotifier. Event bridging via pipe fd.

### [2026-03-31T20:50] Phase 2+3 Progress — Compilation complete

All compilation errors resolved. Key fixes:
- Added `FileReadWriteVolatile` to macOS TapT trait
- Ported `handle_rx_token`/`handle_rx_queue` Worker methods to macOS
- Added `RxDescriptorsExhausted` and `WriteBuffer` to macOS cfg gates
- Fixed vhost-user-net stub signatures (start_queue args + IntoAsync)
- cfg-gated vhost-user-net SubCommand on macOS
- Added macOS net cmdline block

Build: `cargo build --no-default-features --features net --release` ✅
Runtime: requires `sudo` for vmnet — untested (needs user to run interactively)

### [2026-03-31T22:50] Phase 4 — End-to-end vmnet networking

**5 bugs found and fixed:**

| # | Bug | Root Cause | Fix |
|---|-----|-----------|-----|
| 1 | vmnet_start_interface returns NULL | `VMNET_SHARED_MODE` defined as `1` in Rust, actual value is `1001` | `vmnet_tap.rs:61` — changed constant to `1001` |
| 2 | `CONFIG_VIRTIO_NET` missing from built kernel | `CONFIG_NETDEVICES=y` missing from defconfig (parent toggle) | Added to `aetheria_arm64_defconfig` |
| 3 | TX EBADF (Bad file descriptor) | `volatile_impl!` macro routes I/O through pipe fd, not vmnet FFI | Manual `FileReadWriteVolatile` impl using vmnet_helper_read/write |
| 4 | Guest uses wrong MAC address | `None` passed as mac_addr to `Net::new()` | Pass `tap.mac_address().ok()` from VmnetTap |
| 5 | RX never called — zero packets received | `RxTap` WaitContext only registered for Linux/Android, not macOS | Added `target_os = "macos"` to cfg gate in `net.rs:384` |
| 6 | vnet_hdr corruption — DHCP discover garbled | Guest prepends 12-byte `virtio_net_hdr_v1`; vmnet expects raw Ethernet | TX: strip header via `reader.consume(12)`; RX: prepend zeroed header |

**Result**: DHCP lease obtained: `192.168.64.86` from `192.168.64.1`

### Review: Phase 4

| # | Expected Result | Actual Result | Evidence | Verdict |
|---|-----------------|---------------|----------|---------|
| 1 | vmnet interface created | SUCCESS, status=1000 | `vmnet: started shared interface mac=... mtu=1500` | PASS |
| 2 | Kernel sees virtio-net | PCI device enumerated, driver probes | `pci 0000:00:02.0: [1af4:1041]`, `virtio_net virtio1` | PASS |
| 3 | DHCP lease | `192.168.64.86` from `192.168.64.1` | `udhcpc: lease of 192.168.64.86 obtained` | PASS |
| 4 | TX/RX functional | Both directions working | TX: valid Ethernet frames, RX: DHCP offer received | PASS |

**Overall Verdict**: PASS
**Findings this phase**: 6 (F-002 through F-007)

## Plan Corrections

## Findings

### F-002: VMNET_SHARED_MODE constant value
Apple's vmnet.h defines `VMNET_SHARED_MODE = 1001`, not `1`. The Rust constant was incorrectly set to `1`, causing immediate NULL return from vmnet_start_interface.

### F-003: CONFIG_NETDEVICES parent toggle
`CONFIG_VIRTIO_NET=y` requires `CONFIG_NETDEVICES=y` as a parent toggle in Kconfig. Without it, the kernel silently ignores the VIRTIO_NET setting.

### F-004: volatile_impl! incompatible with vmnet
The `base::volatile_impl!` macro generates `FileReadWriteVolatile` using `libc::read/write` on `as_raw_fd()`. For VmnetTap, this fd is the notification pipe (read end), not a device fd. Writing to a pipe read end = EBADF. Must implement the trait manually using vmnet FFI.

### F-005: RxTap WaitContext platform gate
The RxTap event source registration in `net.rs` uses `#[cfg(any(target_os = "android", target_os = "linux"))]`, excluding macOS. Without RxTap registration, the worker thread never polls for incoming packets.

### F-006: vnet_hdr mismatch between virtio and vmnet
Virtio-net with `VIRTIO_F_VERSION_1` always prepends a 12-byte `virtio_net_hdr_v1` to packets. Linux TAP with `IFF_VNET_HDR` handles this transparently. vmnet.framework expects raw Ethernet frames. TX must strip the header; RX must prepend a zeroed header.

### F-007: HVF does not interfere with vmnet
Tested: vmnet_start_interface works before, during, and after hv_vm_create. The Hypervisor.framework and vmnet.framework are independent — no interference.
