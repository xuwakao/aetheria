# Plan: crosvm-hvf-fixes

Created: 2026-03-30T03:30:00+08:00
Status: COMPLETED
Source: [progress/crosvm-hvf-review], review FAIL items from Phases 2-4

## Task Description

Fix remaining FAIL items from the crosvm-hvf review. Critical bugs (F-001 through F-003) were already fixed. This plan addresses the remaining failures:

1. EventContext — real kqueue implementation (Phase 4 FAIL)
2. Unit tests for HVF backend (Phase 2/3 FAIL) — compile-time tests only (runtime needs HVF hardware)
3. crosvm main binary compilation attempt on macOS (Phase 4 FAIL)

Items explicitly deferred (not in this plan):
- cros_async kqueue backend — large effort, not needed for hypervisor crate or initial boot
- get_system_regs completeness — needs runtime investigation of HVF sys reg enumeration
- IPA bits query — needs Apple documentation research

## Alternatives & Trade-offs

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| A: Implement real kqueue EventContext, then try crosvm binary build | Unblocks the full crosvm binary; systematic | EventContext alone is ~300 lines of kqueue code | **Selected** |
| B: Skip EventContext, try to build crosvm binary with stubs | Faster to discover other blockers | EventContext WILL be needed; stubs cause silent failures | Rejected — violates RULE 5a (no workarounds) |
| C: Skip to Phase 5 boot test using QEMU instead of crosvm | Validates kernel works | Doesn't validate our HVF code at all | Rejected — wrong goal |

## Phases

### Phase 1: EventContext kqueue Implementation

**Objective**: Replace the no-op EventContext stub with a real kqueue-based implementation that provides epoll-equivalent functionality.

**Expected Results** (all must be real implementations, not stubs):
- [ ] `EventContext::new()` creates a kqueue fd
- [ ] `EventContext::build_with()` creates kqueue and registers initial fd/token pairs
- [ ] `add_for_event()` calls `kevent` to register EVFILT_READ/EVFILT_WRITE with EV_ADD
- [ ] `modify()` calls `kevent` with EV_ADD to update the filter (kqueue EV_ADD overwrites existing)
- [ ] `delete()` calls `kevent` with EV_DELETE
- [ ] `wait()` calls `kevent` with no timeout, returns `SmallVec<TriggeredEvent<T>>`
- [ ] `wait_timeout()` calls `kevent` with timespec, returns triggered events or empty on timeout
- [ ] `as_raw_descriptor()` returns the kqueue fd
- [ ] Compile test: `cargo build -p base` passes on macOS
- [ ] Functional test: write a test that registers an Event, signals it, and verifies wait() returns it

**Risks**:
- kqueue's token mechanism differs from epoll's `data.u64` — kqueue uses `udata` (pointer-sized). Need to store EventToken data in udata. This is how the existing `kqueue.rs` works (ident field).
- kqueue EVFILT_READ vs EVFILT_WRITE semantics may differ slightly from epoll EPOLLIN/EPOLLOUT for some fd types.

**Dependencies**: None

**Status**: COMPLETE

### Phase 2: Compile-Time Verification Tests

**Objective**: Add compile-time tests that verify HVF types implement all required traits and construct correctly (without needing HVF hardware at runtime).

**Expected Results**:
- [ ] Test file `hypervisor/src/hvf/tests.rs` exists
- [ ] Test `hvf_types_implement_traits` — static assertion that Hvf: Hypervisor, HvfVm: Vm+VmAArch64, HvfVcpu: Vcpu+VcpuAArch64
- [ ] Test `ffi_constants_correct` — verify EC constants match ARM spec values
- [ ] Test `syndrome_parsing` — verify data_abort_iswrite, data_abort_sas, data_abort_srt with known syndrome values from QEMU source
- [ ] `cargo test -p hypervisor --features hvf` passes (test compilation, not HVF runtime)

**Risks**:
- Some tests may require `#[cfg(test)]` gating to avoid linking HVF at test time if running on Linux CI.

**Dependencies**: Phase 1 (for base crate stability)

**Status**: COMPLETE

### Phase 3: crosvm Main Binary Build Attempt

**Objective**: Attempt to build the full crosvm binary on macOS and document all remaining compilation failures.

**Expected Results**:
- [ ] `cargo build --features hvf` (full workspace) attempted
- [ ] All compilation errors documented with crate name, error type, and estimated fix effort
- [ ] If successful: binary exists and can be code-signed with entitlements
- [ ] If not successful: issue document with complete list of remaining macOS blockers

**Risks**:
- The devices, arch, and crosvm main crate likely have many more Linux-specific dependencies.
- This phase is diagnostic — it may produce a FAIL result that becomes input for a future plan.

**Dependencies**: Phase 1, Phase 2

**Status**: COMPLETE (diagnostic — 30 crates need macOS modules, documented)

## Findings
