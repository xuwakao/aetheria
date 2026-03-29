# Progress: crosvm-macos-real-impl

Created: 2026-03-30T18:30:00+08:00
Source: [plan/crosvm-macos-real-impl]

## Log

### [2026-03-30T18:30] META-PHASE A — Planning
**Action**: Created plan based on comprehensive audit of 14 macOS stub files. Classified stubs into IMPLEMENT (4 cros_async + 1 console), KEEP (6 intentional platform stubs), and DEFER (3 requiring platform research).
**Result**: Plan created with 3 phases.

### [2026-03-30T18:45] META-PHASE B — Plan Review

| # | Item | Verdict | Evidence |
|---|------|---------|----------|
| 1 | Dependency validation | PASS | Phase 1 has no deps; Phase 2 uses base kqueue (not cros_async); Phase 3 deps on Phase 1. No circular deps. |
| 2 | Expected results precision | PASS | Each has specific compile/test commands and "real impl, not stub" markers |
| 3 | Feasibility | PASS | EpollReactor is 397 lines; kqueue maps 1:1 (kevent↔epoll_ctl, kevent wait↔epoll_wait). `Reactor` trait has 7 methods. PollSource is ~300 lines, reusable pattern. |
| 4 | Risk identification | RISK | cros_async uses `pread64`/`pwrite64` which are Linux-specific — macOS has `pread`/`pwrite`. Need libc compat aliases. |
| 5 | Stub vs real | PASS | Plan explicitly marks KEEP stubs (ACPI, udmabuf, IOMMU, vhost_user_backend) with rationale |
| 6 | Alternatives | PASS | Three alternatives evaluated with evidence |

**Risk mitigation for #4**: macOS uses `pread`/`pwrite` and `off_t` instead of `pread64`/`pwrite64`/`off64_t`. The KqueueSource will use macOS-native syscalls directly.

### [2026-03-30T18:50] Starting Phase 1 — cros_async kqueue executor
**Expected results**: KqueueReactor implementing Reactor trait, KqueueSource implementing async I/O, EventAsync/TimerAsync/AsyncTube working, tests pass, workspace compiles.

### Review: Phase 1

| # | Expected Result | Actual Result | Evidence | Verdict |
|---|-----------------|---------------|----------|---------|
| 1 | `executor.rs` — real KqueueReactor implementing Reactor trait | KqueueReactor implemented: kqueue fd, wake pipe, add_operation/is_ready/cancel_operation/wait_for_work/wake | `cros_async/src/sys/macos/kqueue_reactor.rs` (320 lines) | PASS |
| 2 | `event.rs` — EventAsync::new() succeeds, next_val() returns values | EventAsync::new() creates IoSource via executor, next_val() reads 8 bytes from event fd | `cros_async/src/sys/macos/event.rs` | PASS [UNVERIFIED at runtime — no macOS eventfd, would need pipe-based Event to test] |
| 3 | `timer.rs` — TimerAsync::wait_sys() waits for timer expiry | TimerAsync::wait_sys() reads from timer fd via IoSource | `cros_async/src/sys/macos/timer.rs` | PASS [UNVERIFIED at runtime — depends on base Timer fd implementation] |
| 4 | `async_types.rs` — AsyncTube::new() succeeds, next()/send() perform real I/O | AsyncTube wraps IoSource<Tube>, next() waits readable then recv(), send() calls tube.send() | `cros_async/src/sys/macos/async_types.rs` | PASS [UNVERIFIED at runtime] |
| 5 | `cargo test -p cros_async` passes | 57 tests pass, 0 failures (55 lib + 2 integration) | `test result: ok. 57 passed; 0 failed` | PASS |
| 6 | `cargo build --no-default-features` compiles full workspace | Full workspace compiles (0 errors, warnings only) | `Finished dev profile [unoptimized + debuginfo]` | PASS |

**Overall Verdict**: PASS
**Notes**: EventAsync/TimerAsync/AsyncTube are structurally correct (follow same pattern as Linux) but their runtime behavior depends on base crate's Event/Timer fd implementations on macOS. The kqueue executor itself is verified by the integration tests (cancel_pending_task, cancel_ready_task).

### [2026-03-30T19:10] Phase 1 — Functional Acceptance
**Build**: PASS — `cargo build -p cros_async --no-default-features` and `cargo build --no-default-features`
**Tests**: PASS — 57/57 tests pass
**Evidence**: `test result: ok. 57 passed; 0 failed; 0 ignored; 0 measured`

### [2026-03-30T19:15] Starting Phase 2 — Console input thread
**Expected results**: `spawn_input_thread()` runs a proper read loop with fd readiness waiting, no busy-polling, `cargo build -p devices --no-default-features` compiles.

### Review: Phase 2

| # | Expected Result | Actual Result | Evidence | Verdict |
|---|-----------------|---------------|----------|---------|
| 1 | `spawn_input_thread()` runs proper read loop with fd readiness waiting | Implemented using WaitContext (kqueue) with ConsoleEvent and Kill tokens, 4KB read buffer, kill timeout drain logic | `devices/src/virtio/console/sys/macos.rs` (105 lines, mirrors Linux implementation) | PASS [UNVERIFIED at runtime] |
| 2 | Console input is responsive (no busy-polling) | Uses WaitContext::wait() which blocks on kqueue — no polling | Same file, `wait_ctx.wait()` call at line 67 | PASS [UNVERIFIED at runtime] |
| 3 | `cargo build -p devices --no-default-features` compiles | Compiles with 0 errors, 38 warnings | `Finished dev profile [unoptimized + debuginfo]` | PASS |

**Overall Verdict**: PASS
**Notes**: Implementation is structurally identical to Linux version. Runtime verification requires a running VM with console input, which is not yet possible (needs HVF runtime wiring).

### [2026-03-30T19:20] Phase 2 — Functional Acceptance
**Build**: PASS — `cargo build -p devices --no-default-features` and full workspace compile
**Evidence**: `Finished dev profile [unoptimized + debuginfo]`

### [2026-03-30T19:25] Phase 3 — DEFERRED: Network module is feature-gated
**Action**: Investigated `devices/src/virtio/net/` — entire module is behind `#[cfg(feature = "net")]` and NOT compiled with `--no-default-features`. The macOS stub file is dead code. vmnet.framework implementation deferred until networking is actually needed.
**Cross-ref**: [plan/crosvm-macos-real-impl#Phase3]

### F-004: Feature-gated stubs are not on the critical path
Multiple stub files (net, vsock, vhost_user_backend) are behind Cargo features that are disabled with `--no-default-features`. These stubs are never compiled and cannot block VM execution. Only stubs in unconditionally-compiled modules need real implementations.

### [2026-03-30T19:30] META-PHASE D — Completion
**Summary**: Phases 1-2 COMPLETE, Phase 3 DEFERRED (feature-gated, non-blocking).

Phase 1 (kqueue executor): KqueueReactor + KqueueSource replace all 4 cros_async stubs. 57/57 tests pass.
Phase 2 (console input): Real WaitContext-based read loop replaces single-check stub. Mirrors Linux implementation.
Phase 3 (network): DEFERRED — behind `#[cfg(feature = "net")]`, not blocking.

**Remaining intentional stubs (kept by design)**:
- `devices/sys/macos/acpi.rs` — macOS has no ACPI subsystem
- `devices/virtio/iommu/sys/macos.rs` — PCI passthrough only
- `vm_memory/udmabuf/sys/macos.rs` — Linux kernel feature
- `devices/virtio/vhost_user_backend/*/sys/macos.rs` — feature-gated, not compiled
- `devices/virtio/vhost_user_frontend/sys/macos.rs` — feature-gated

## Plan Corrections

## Findings
