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

## Plan Corrections

## Findings
