# Progress: crosvm-macos-port

Created: 2026-03-30T05:30:00+08:00
Source: [plan/crosvm-macos-port]

## Log

### [2026-03-30T05:30] META-PHASE A - Planning
**Action**: Created plan based on deep analysis of crosvm dependency chain. Identified exactly 7 missing sys modules. Structured 4 phases bottom-up by crate dependency order.
**Result**: PASS
**Cross-ref**: [plan/crosvm-macos-port]

### [2026-03-30T05:32] META-PHASE B - Plan Review Checklist

**1. Dependency validation**: Phase 1→2→3→4 linear. Phase 2 depends on Phase 1 (crosvm_cli/metrics_events must compile before devices which may import them). Phase 3 depends on Phase 2 (main binary imports arch/devices/vm_control). PASS.

**2. Expected results precision**: Each phase has `cargo build -p <crate>` as concrete test. Phase 3 has `cargo build --no-default-features` for full workspace. Phase 4 has `codesign` and `--help` smoke test. All verifiable. PASS.

**3. Feasibility**: Phase 1 trivial (empty branches). Phase 2 moderate (copy+adapt). Phase 3 large but the 5552-line module is 96% POSIX-portable per analysis (only 4 Linux cfg guards). RISK: devices sub-modules may cascade. Acceptable — documented in Phase 2 risks.

**4. Risk identification**: Main risk is Phase 2/3 cascading into more sub-module fixes. Mitigation: fix iteratively within each phase, document each sub-fix.

**5. Stub vs real**: Plan specifies "real impl where POSIX, stub where Linux-only" for each module. PASS.

**6. Alternatives**: Three evaluated with evidence (previous attempt failed). PASS.

## Plan Corrections

## Findings

### [2026-03-30T05:35] Phase 1 - COMPLETE: Trivial modules already compile
**Action**: Verified `crosvm_cli` and `metrics_events` compile on macOS without changes — their sys.rs only has Windows branches, macOS falls through to no platform code which is correct.
**Result**: PASS — no changes needed
**Evidence**: `cargo build -p crosvm_cli`: Finished; `cargo build -p metrics_events`: Finished
**Cross-ref**: [plan/crosvm-macos-port#Phase1]

### [2026-03-30T06:00] Phase 2 - IN PROGRESS: Fixed disk (FileAllocate), vm_control (full macOS module), base (macos re-export). devices has 14 sub-modules needing macOS + additional base/cros_async issues
**Action**: 
  - Fixed disk: added `base/src/sys/macos/file_traits.rs` (FileAllocate via F_PREALLOCATE). `cargo build -p disk` passes.
  - Fixed vm_control: full macOS sys module (handle_request, VmMemoryMapping, FsMapping, prepare_shared_memory_region). `cargo build -p vm_control` passes.
  - Fixed base: added `pub use sys::macos` re-export for MemoryMappingBuilderUnix.
  - `devices` has 14 sub-modules needing macOS modules + ioctl macro imports + serial_device + evdev + scsi issues. ~70 errors across devices+cros_async+base.
**Result**: PARTIAL — disk and vm_control done, devices is the next large blocker
**Cross-ref**: [plan/crosvm-macos-port#Phase2]

### F-002: devices crate is the deepest blocker
The `devices` crate has 14 sub-module sys dispatches without macOS modules (vsock, net, console, serial, iommu, vhost_user_backend x6, vhost_user_frontend). Even with `--no-default-features`, the serial_device, pci, block, input/evdev modules are unconditionally compiled and have Linux-specific imports. This is the single largest remaining porting effort.
