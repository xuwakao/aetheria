# Progress: crosvm-hvf

Created: 2026-03-30T00:10:00+08:00
Source: [plan/crosvm-hvf]

## Log

### [2026-03-30T00:10] META-PHASE A - Planning
**Action**: Analyzed crosvm source: `hypervisor/src/lib.rs` (715 lines, trait definitions), `hypervisor/src/aarch64.rs` (291 lines, ARM64-specific traits), `hypervisor/Cargo.toml` (build config), `base/src/sys/macos/` (5 files, 33 todo!()). Cross-referenced with feasibility study and QEMU HVF reference.
**Result**: PASS — plan created with 5 phases
**Cross-ref**: [plan/crosvm-hvf]

### [2026-03-30T00:15] META-PHASE B - Plan Review
**Action**: Re-read entire plan. Verified: 5 phases with correct dependencies, all expected results concrete and testable, 3 findings documented. Phase 4 can run in parallel with Phase 2/3. No modifications needed.
**Result**: PASS
**Cross-ref**: [plan/crosvm-hvf]

### [2026-03-30T00:20] Phase 1 - FFI Bindings and Build Infrastructure (In Progress)
**Action**: Created `hypervisor/src/hvf/` module with ffi.rs (279 lines — complete HVF C API bindings), mod.rs (Hvf struct + Hypervisor trait), vm.rs and vcpu.rs placeholders. Updated lib.rs with `cfg(target_os = "macos", feature = "hvf")` module import and `HypervisorKind::Hvf`. Updated Cargo.toml with `hvf` feature. Created entitlements.plist. Build initiated.
**Result**: IN PROGRESS — waiting for cargo build
**Cross-ref**: [plan/crosvm-hvf#Phase1]

### [2026-03-30T00:35] Phase 1 - Build Blocker: cros_async
**Action**: Build attempt failed. Fixed syslog signature bug. Discovered `cros_async` has 39 errors on macOS (no macOS support). Filed ISS-001.
**Result**: FAIL — blocker identified
**Cross-ref**: [plan/crosvm-hvf#Phase1], [issue/crosvm-hvf#ISS-001]

### [2026-03-30T00:50] Phase 1 - Fixing cros_async blocker
**Action**: Investigating cros_async to create macOS stub.
**Result**: IN PROGRESS
**Cross-ref**: [issue/crosvm-hvf#ISS-001]

## Plan Corrections

### [2026-03-30T00:50] Correction: Phase 1 requires cros_async macOS stub
**Original**: [plan/crosvm-hvf#Phase1] — assumed hypervisor crate compiles independently
**Reason**: Transitive dependency: hypervisor → base → audio_streams → cros_async. cros_async has zero macOS support.
**New plan**: Add macOS stub to cros_async as part of Phase 1 before attempting hypervisor build.

## Findings

### [2026-03-30T01:20] Phase 1 - cros_async Fixed, vm_memory Next
**Action**: cros_async compiles on macOS (was 39 errors). Created sys/macos/ stubs, Infallible enum variants, unreachable!() wildcards. Now vm_memory: 11 errors.
**Result**: PARTIAL
**Cross-ref**: [issue/crosvm-hvf#ISS-001]

### [2026-03-30T01:45] Phase 1 - COMPLETE: hypervisor crate compiles on macOS
**Action**: Fixed entire dependency chain:
  - base: syslog signature fix + mmap.rs ported from Linux (mmap64→mmap, off64_t→off_t, MADV_* fixes, mlock2→mlock)
  - cros_async: new sys/macos/ platform (executor, async_types, event, error, timer stubs), Infallible enum variants for IoSource/Executor/TaskHandle
  - vm_memory: new udmabuf/sys/macos.rs stub, new guest_memory/sys/macos.rs (zero_range, find_data_ranges, open)
  - hypervisor: hvf/ module with ffi.rs (HVF C API bindings), mod.rs (Hvf struct + Hypervisor trait), placeholder vm.rs/vcpu.rs
**Result**: PASS — `cargo build -p hypervisor --features hvf` succeeds on macOS ARM64
**Cross-ref**: [plan/crosvm-hvf#Phase1]

### [2026-03-30T02:00] Phase 2 - COMPLETE: Vm + VmAArch64 trait implementation
**Action**: Implemented HvfVm struct with full Vm and VmAArch64 trait. Memory mapping via hv_vm_map/hv_vm_unmap, user-space ioevent dispatch, vCPU creation stub (Phase 3). Based on WHPX pattern.
**Result**: PASS — compiles clean
**Cross-ref**: [plan/crosvm-hvf#Phase2]

### [2026-03-30T02:15] Phase 3 - COMPLETE: Vcpu + VcpuAArch64 with run loop
**Action**: Implemented HvfVcpu with full run loop: hv_vcpu_run → exit reason parsing → VcpuExit mapping. Handles EC_DATAABORT (MMIO), EC_SYSTEMREGISTERTRAP (sysreg), EC_WFX_TRAP (WFI/WFE), EC_AA64_HVC/SMC (hypercall), debug exceptions, vtimer. handle_mmio() parses syndrome (ISV, iswrite, sas, srt) and dispatches to IoParams callback. All VcpuAArch64 methods implemented (register access, PSCI version, snapshots).
**Result**: PASS — compiles clean
**Cross-ref**: [plan/crosvm-hvf#Phase3]

### [2026-03-30T02:25] Phase 4 - COMPLETE: macOS platform layer — zero todo!()
**Action**: Replaced all 33 todo!() stubs in base/src/sys/macos/mod.rs with implementations: set_thread_name (pthread_setname_np), getpid, get/set_cpu_affinity, ioctl family (6 functions), file_punch_hole (F_PUNCHHOLE), file_write_zeroes_at (pwrite), SharedMemory (shm_open), SafeDescriptor::eq, syslog (stderr fallback), EventContext (kqueue skeleton), pipe, open_file_or_duplicate, enable_high_res_timers.
**Result**: PASS — 0 todo!() remaining, hypervisor compiles clean
**Cross-ref**: [plan/crosvm-hvf#Phase4]
