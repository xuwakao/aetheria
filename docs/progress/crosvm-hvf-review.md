# Review: crosvm-hvf Phases 1-4

Created: 2026-03-30T03:00:00+08:00
Source: [plan/crosvm-hvf], retrospective of Phases 1-4

## Phase 1: FFI Bindings and Build Infrastructure

### Expected Results vs Actual

| Expected Result | Verdict | Evidence |
|----------------|---------|----------|
| `hvf/ffi.rs` with complete HVF C API bindings | **PASS** | 316 lines, 20 extern "C" functions, covers VM/vCPU/register/interrupt/timer APIs |
| `Cargo.toml` has `hvf` feature flag | **PASS** | `hvf = []` in `[features]` |
| `lib.rs` has conditional `pub mod hvf` | **PASS** | `#[cfg(all(target_os = "macos", target_arch = "aarch64", feature = "hvf"))]` |
| `HypervisorKind::Hvf` added | **PASS** | Present in enum |
| Entitlements plist created | **PASS** | `entitlements.plist` with `com.apple.security.hypervisor` |
| `cargo build --features hvf` compiles | **PASS** | `Finished dev profile` confirmed |

### Issues Found

**ISS-001 (filed)**: `cros_async` had zero macOS support (39 errors). Required creating `sys/macos/` platform stubs with Infallible enum variants. **This was a workaround, not a real implementation.** The cros_async macOS module contains:
- `Executor` with `_Macos(Infallible)` — cannot be constructed
- `TaskHandle` with `_Macos(Infallible, PhantomData)` — cannot be constructed
- `IoSource` with `_Macos(Infallible, PhantomData)` — cannot be constructed
- All async methods return `unreachable!()` or `Err(Unsupported)`

**Verdict for this workaround**: Acceptable for Phase 1's scope (compile hypervisor crate), but must be flagged as technical debt. The cros_async macOS module is NOT functional — it only satisfies the type checker.

### Phase 1 Overall: **PASS** (all 6 expected results met)

---

## Phase 2: Hypervisor + Vm Trait Implementation

### Expected Results vs Actual

| Expected Result | Verdict | Evidence |
|----------------|---------|----------|
| `mod.rs` implements Hypervisor trait (Hvf) | **PASS** | `impl Hypervisor for Hvf` — `try_clone`, `check_capability` |
| `vm.rs` implements Vm + VmAArch64 (HvfVm) | **PASS** | Both traits fully implemented (263 lines) |
| VM creation: `hv_vm_create` | **PASS** | Called in `Hvf::new()`, `HvfVm::new()` wraps the result |
| Memory mapping: `add_memory_region` → `hv_vm_map` | **PASS** | Calls `hv_vm_map` with correct flags, tracks slots |
| Memory unmapping: `remove_memory_region` → `hv_vm_unmap` | **PASS** | Calls `hv_vm_unmap` with correct address/size |
| vCPU creation: `create_vcpu` → `hv_vcpu_create` | **PASS** | `VmAArch64::create_vcpu` → `HvfVcpu::new(id)` |
| ioevent registration: user-space dispatch | **PASS** | `FnvHashMap<IoEventAddress, Event>`, signal on match |
| Unit test: create VM, map memory, destroy | **FAIL** | No unit test written |

### Code Quality Issues

1. **`get_guest_phys_addr_bits` hardcoded to 36**: Comment says "some chips support 40-bit". Should query HVF at runtime or at least use a higher value. Apple M1+ supports 36-bit IPA by default but can be configured. `[待验证]` — need to check Apple documentation for the correct IPA range.

2. **`Hvf` stored in `HvfVm` but `Hvf::drop` calls `hv_vm_destroy`**: If `HvfVm` is cloned via `try_clone()`, both clones hold `Hvf` instances. When the first drops, `hv_vm_destroy` is called, invalidating the second. **This is a bug.** The `Hvf` struct should use reference counting and only call `hv_vm_destroy` when the last reference drops.

3. **`handle_balloon_event` silently succeeds**: Returns `Ok(())` but does nothing. Should at least log that balloon is not implemented. Not a functional bug but misleading.

4. **No `Datamatch` checking in `register_ioevent`**: The `_datamatch` parameter is ignored. WHPX also does this, so it's acceptable for now.

### Phase 2 Overall: **FAIL** — missing unit test, Hvf Drop bug

---

## Phase 3: Vcpu Trait Implementation

### Expected Results vs Actual

| Expected Result | Verdict | Evidence |
|----------------|---------|----------|
| `vcpu.rs` implements Vcpu + VcpuAArch64 | **PASS** | Both traits (375 lines) |
| `run()` translates EC_DATAABORT → Mmio | **PASS** | Lines 148-151, handles SAME_EL variant |
| `run()` translates EC_SYSTEMREGISTERTRAP → MsrAccess | **PASS** | Lines 153-155 |
| `run()` translates EC_WFX_TRAP → Hlt | **PASS** | Lines 157-166, WFE vs WFI distinguished |
| `run()` translates EC_AA64_HVC → Hypercall | **PASS** | Line 168 |
| `run()` translates EC_AA64_SMC → Hypercall + advance PC | **PASS** | Lines 169-172 |
| `run()` translates debug exceptions → Debug | **PASS** | Lines 174-180, all 7 EC codes covered |
| `handle_mmio()` parses syndrome correctly | **PASS with concerns** | ISV/iswrite/sas/srt parsing matches QEMU reference |
| `set_one_reg`/`get_one_reg` wraps HVF regs | **PASS** | Lines 279-304, GP regs + system regs |
| `init()` configures PSCI | **PASS** | No-op (PSCI via HVC/SMC exits, not config) — acceptable |
| PC advance (+4) for trapped instructions | **PASS** | `advance_pc()` called for WFI/WFE, SMC, MMIO |
| Unit test: create VM, create vCPU, run | **FAIL** | No unit test written |

### Code Quality Issues

1. **`unsafe impl Send for HvfVcpu` and `unsafe impl Sync for HvfVcpu`**: The comment says "caller must ensure single-threaded access per vCPU". This is correct for the Send bound (crosvm moves vCPUs to dedicated threads), but **Sync is incorrect and dangerous**. `Sync` means safe for shared references across threads, but HVF vCPUs are explicitly single-threaded. Should only impl `Send`, not `Sync`.

2. **`Sp` register mapping**: Line 112 maps `VcpuRegAArch64::Sp` to `HV_REG_X0 + 31`. But HVF's register numbering for SP may not be X31 — Apple's HVF documentation does not guarantee SP = X31 in the `hv_reg_t` enum. `[待验证]` — need to verify HVF's actual SP register ID.

3. **`get_system_regs` returns empty BTreeMap**: The function reads MPIDR but discards the result (line 334: `let _ = ...`). This means snapshot/restore will lose all system register state. This is a significant functional gap for live migration, but acceptable for initial boot.

4. **`handle_mmio` reads from `exit_info` pointer**: The raw pointer `exit_info` is valid only between `hv_vcpu_run` calls. If `handle_mmio` is called at any other time, it reads stale/invalid data. **This matches the trait contract** ("should be called after `Vcpu::run` returns `VcpuExit::Mmio`"), so it's correct but fragile.

5. **`set_immediate_exit` uses `AtomicBool` but `hv_vcpu_run` is not interrupted**: When `set_immediate_exit(true)` is called from another thread while `hv_vcpu_run` is blocking, the vCPU won't exit until the next natural exit. The proper implementation should call `hv_vcpu_cancel()` (not present in FFI bindings) to force an immediate exit. **Missing `hv_vcpu_cancel` in ffi.rs is a bug.**

### Phase 3 Overall: **FAIL** — missing unit test, Sync unsound, missing hv_vcpu_cancel

---

## Phase 4: macOS Platform Layer

### Expected Results vs Actual

| Expected Result | Verdict | Evidence |
|----------------|---------|----------|
| All `todo!()` replaced | **PASS** | grep count = 0 |
| EventContext using kqueue | **FAIL** | All methods are no-ops or return empty: `wait()` returns `SmallVec::new()`, `add_for_event` does nothing. Not a kqueue implementation. |
| MemoryMapping using mmap | **PASS** | Ported from Linux `mmap.rs`, adapted POSIX differences |
| SharedMemory using shm_open | **PASS** | Uses `shm_open` + `ftruncate`, unlinks immediately |
| getpid, set_thread_name, etc. | **PASS** | Real implementations using libc |
| ioctl family | **PASS** | Direct libc::ioctl calls (6 functions) |
| file_punch_hole, file_write_zeroes_at | **PASS** | `F_PUNCHHOLE` and `pwrite` respectively |
| crosvm main binary compiles | **FAIL** | Never attempted — only `hypervisor` crate verified |

### Code Quality Issues

1. **EventContext is a no-op stub disguised as an implementation**: The `todo!()` markers were removed but replaced with empty implementations that do nothing. `wait()` immediately returns an empty list — any code depending on EventContext will silently hang or busy-loop. **This should have been marked as a stub, not "implemented".**

2. **SharedMemory `new()` accesses `crate::SharedMemory` fields directly**: Line `Ok(crate::SharedMemory { descriptor, size })` assumes SharedMemory's fields are `pub(crate)`. This may be fragile if SharedMemory's definition changes. `[待验证]` — need to check if this compiles with the full crosvm build.

3. **`file_punch_hole` uses `F_PUNCHHOLE`**: This requires macOS 10.12+ and the file to support it (not all filesystems). No error handling for unsupported filesystems.

4. **`syslog::new` returns `(None, None)`**: This means no logging platform is configured. All syslog output will be silently dropped. Should at minimum configure stderr output.

### Phase 4 Overall: **FAIL** — EventContext is stub, crosvm main binary not verified

---

## Summary

| Phase | Verdict | Reason for FAIL |
|-------|---------|-----------------|
| Phase 1 | **PASS** | All 6 expected results met (cros_async stubs acceptable for compile target) |
| Phase 2 | **FAIL** | Missing unit test; Hvf Drop bug with try_clone |
| Phase 3 | **FAIL** | Missing unit test; unsound `Sync` impl; missing `hv_vcpu_cancel` |
| Phase 4 | **FAIL** | EventContext is stub not implementation; crosvm main binary not verified |

## Critical Bugs Found

1. **Hvf Drop double-destroy** (Phase 2): `HvfVm::try_clone()` creates a new `Hvf` instance. Both will call `hv_vm_destroy` on drop. Second call will fail or cause undefined behavior.

2. **Unsound `unsafe impl Sync for HvfVcpu`** (Phase 3): HVF vCPUs are single-threaded. `Sync` allows shared references from multiple threads, which would violate HVF's threading model.

3. **Missing `hv_vcpu_cancel`** (Phase 3): `set_immediate_exit` cannot interrupt a blocking `hv_vcpu_run`. Need to add `hv_vcpu_cancel` to FFI bindings and call it.

## Technical Debt

1. **cros_async macOS module**: Entire module is compile-only stubs. No async I/O works.
2. **EventContext**: No-op stub, not a real kqueue implementation.
3. **get_system_regs**: Returns empty — snapshot/restore broken.
4. **IPA bits hardcoded**: Should query or use correct Apple Silicon value.

## Findings

### F-001: Hvf Drop Safety
The `Hvf` struct calls `hv_vm_destroy` in its `Drop` impl but can be duplicated via `HvfVm::try_clone()`. This needs refcounting or removing Drop from Hvf and managing VM lifecycle explicitly.

### F-002: HVF Threading Model
HVF binds vCPUs to the creating pthread. `unsafe impl Sync` is unsound. Only `Send` is needed (to move the vCPU to its dedicated thread once).

### F-003: hv_vcpu_cancel Missing
Apple's Hypervisor.framework provides `hv_vcpu_cancel()` for forcing a vCPU to exit from another thread. This is needed for `set_immediate_exit` to work correctly. Currently missing from ffi.rs.

---

## Bug Fix Log

### [2026-03-30T03:20] Fix: Hvf Drop double-destroy (F-001)
**Root cause**: `Hvf` struct had `Drop` impl calling `hv_vm_destroy`, but `HvfVm::try_clone` and `HvfVm::new` created new `Hvf` instances.
**Fix**: Replaced bare `Hvf` with `Arc<HvfVmGuard>` pattern. `HvfVmGuard` calls `hv_vm_destroy` on drop. `Hvf::try_clone()` clones the Arc. `HvfVm::new()` takes `Hvf` by value. `hv_vm_destroy` called exactly once.
**Verified**: Compiles clean.

### [2026-03-30T03:20] Fix: Unsound Sync impl (F-002)
**Root cause**: `unsafe impl Sync` was present without justification. Initial review said to remove Sync, but `Vcpu` trait requires `DowncastSync` which bounds `Sync`.
**Fix**: Kept `unsafe impl Sync` but added detailed safety comment explaining why it's sound: `exit_info` pointer is only accessed from the vCPU thread after `hv_vcpu_run` returns, and the only cross-thread operation (`hv_vcpu_cancel`) does not access it.
**Verified**: Compiles clean. Safety argument is sound given crosvm's threading model.

### [2026-03-30T03:20] Fix: Missing hv_vcpu_cancel (F-003)
**Root cause**: `set_immediate_exit` only set an `AtomicBool`, but a blocking `hv_vcpu_run` would not check it until the next natural exit.
**Fix**: Added `hv_vcpu_cancel` to `ffi.rs`. `set_immediate_exit(true)` now calls `hv_vcpu_cancel` via a `VcpuCancelHandle` wrapper (Send+Sync safe, documented by Apple as thread-safe). The `run()` loop also checks the AtomicBool before entering `hv_vcpu_run`.
**Verified**: Compiles clean.
