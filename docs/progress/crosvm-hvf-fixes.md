# Progress: crosvm-hvf-fixes

Created: 2026-03-30T03:30:00+08:00
Source: [plan/crosvm-hvf-fixes]

## Log

### [2026-03-30T03:30] META-PHASE A - Planning
**Action**: Analyzed review FAIL items. Identified 3 phases: EventContext kqueue (core), compile-time tests (verification), crosvm binary build (diagnostic). Deferred cros_async/get_system_regs/IPA as out of scope.
**Result**: PASS
**Cross-ref**: [plan/crosvm-hvf-fixes]

### [2026-03-30T03:35] META-PHASE B - Plan Review Checklist

**1. Dependency validation**:
- Phase 2 depends on Phase 1: PASS — Phase 2 tests need `cargo build -p base` working, which Phase 1 ensures.
- Phase 3 depends on Phase 1+2: PASS — full build attempt needs both EventContext and stable hypervisor crate.
- No circular dependencies: PASS.

**2. Expected results precision**:
- Phase 1: 10 results, all precise ("calls kevent with EV_ADD", "returns kqueue fd"). PASS.
- Phase 2: 5 results, testable (`cargo test` passes). PASS.
- Phase 3: 4 results, includes both success and failure paths. PASS.

**3. Feasibility assessment**:
- Phase 1: kqueue is well-documented, existing `kqueue.rs` in crosvm already wraps kevent64. EventContext maps 1:1 to kqueue operations. **PASS** — feasible.
- Phase 2: Tests that don't call HVF can run anywhere. Need `#[cfg(test)]` or `#[ignore]` for runtime tests. **PASS** — feasible with cfg gating.
- Phase 3: Full crosvm build will likely fail — this is expected and the plan accounts for it (diagnostic phase). **RISK** — may produce a very long list of errors. Set a timebox of 30 minutes for error triage.

**4. Risk identification**:
- Phase 1: kqueue `udata` is `*mut c_void` (pointer-sized) on macOS, vs epoll `data.u64`. Need to cast EventToken's raw value to/from udata. Existing `kqueue.rs` uses `ident` field, not `udata`. Must verify which approach works for arbitrary fd tracking. **RISK — needs investigation during Phase 1.**
- Phase 2: `#[cfg(target_os = "macos")]` on test module means tests won't run on Linux CI. Acceptable for now.
- Phase 3: Could be blocked by dozens of crates. Must document, not fix.

**5. Stub vs real implementation**:
- Phase 1: All expected results specify "calls kevent" — clearly real implementation. PASS.
- Phase 2: "compile-time tests" explicitly stated — no runtime HVF needed. PASS.
- Phase 3: Explicitly labeled "diagnostic" — FAIL result is acceptable. PASS.

**6. Alternatives completeness**:
- Three alternatives evaluated with evidence-based rationale. PASS.

**Review result**: Plan is sound. One risk flagged (kqueue udata mechanism) to investigate in Phase 1.

## Plan Corrections

## Findings

### [2026-03-30T03:50] Phase 1 - COMPLETE: EventContext kqueue implementation
**Action**: Replaced no-op EventContext stub with real kqueue-based implementation (~150 lines). Uses kqueue fd, kevent() for add/modify/delete/wait. Token stored in udata field. Added functional test (event_context_kqueue_basic) that creates events, signals them, and verifies wait() returns correct tokens.
**Result**: PASS — all 10 expected results verified
**Evidence**:
  - `cargo build -p base`: Finished dev profile
  - `cargo test -p base -- event_context_kqueue`: test sys::macos::tests::event_context_kqueue_basic ... ok
**Cross-ref**: [plan/crosvm-hvf-fixes#Phase1]

### [2026-03-30T04:10] Phase 2 - COMPLETE: Compile-time verification tests
**Action**: Created `hypervisor/src/hvf/tests.rs` (164 lines) with 7 tests:
  - Static trait assertions (Hvf:Hypervisor, HvfVm:Vm+VmAArch64, HvfVcpu:Vcpu+VcpuAArch64)
  - ffi_constants_correct: 14 EC/exit/memory/return constant checks against ARM spec
  - syndrome_parsing (5 tests): EC extraction, data abort read/write/byte, WFx WFI/WFE, sysreg read/write
**Result**: PASS — `cargo test -p hypervisor --features hvf`: 7 passed, 0 failed
**Evidence**: `test result: ok. 7 passed; 0 failed; 0 ignored; 0 measured`
**Cross-ref**: [plan/crosvm-hvf-fixes#Phase2]

### [2026-03-30T04:25] Phase 3 - COMPLETE (diagnostic): crosvm binary build blocked by 30 crates
**Action**: Attempted `cargo build --no-default-features` for full crosvm workspace. Fixed `prebuilts` (trivial — curl works on macOS). Build then blocked by `metrics`. Systematic scan found **30 crates** with `sys.rs` platform dispatch but no macOS module.
**Result**: FAIL (expected — this phase is diagnostic)
**Evidence**:
  - `prebuilts`: fixed (1 file copy)
  - `metrics`: missing `run_internal` macOS impl
  - Total missing macOS modules: 30 crates across hypervisor, base, devices, arch, crosvm main
  - Key categories:
    - Core infrastructure (6): metrics, metrics_events, vm_control, crosvm_cli, cros_async/blocking, disk
    - Networking (3): net_util, net_util/slirp, vmm_vhost
    - Devices (12): virtio/vsock, net, console, iommu, serial, vhost_user_* (7 backends)
    - Architecture (3): arch, arch/pstore, arch/serial
    - Main binary (2): src/crosvm/sys, src/sys
    - Other (4): e2e_tests, gpu_display/vulkan, prebuilts (fixed)
**Cross-ref**: [plan/crosvm-hvf-fixes#Phase3]
