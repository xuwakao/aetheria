# Plan: crosvm-hvf

Created: 2026-03-30T00:10:00+08:00
Status: ACTIVE
Source: Architecture decision (memory/architecture_decision.md), feasibility analysis (docs/research/crosvm-hvf-feasibility.md)

## Task Description

Implement the Apple Hypervisor.framework (HVF) backend for crosvm on macOS ARM64 (Apple Silicon), enabling crosvm to serve as the unified VMM for Aetheria across all three platforms. This involves:

1. Creating a new `hypervisor/src/hvf/` module implementing `Hypervisor`, `Vm`, `Vcpu`, `VmAArch64`, `VcpuAArch64` traits
2. Completing the macOS platform abstraction layer (`base/src/sys/macos/` — 33 `todo!()` stubs)
3. Build configuration (Cargo.toml, feature flags, entitlements)
4. Boot verification — crosvm boots our aetheria kernel and outputs to serial console

All work is on the `aetheria-crosvm` submodule (fork of `github.com/xuwakao/crosvm`).

## Alternatives & Trade-offs

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| A: Implement HVF backend from scratch using QEMU hvf.c as reference | Proven reference (2602 lines C); full understanding of exit handling | Must translate C patterns to Rust; must fit crosvm trait API | **Selected** |
| B: Port WHPX backend to HVF (translate Windows API calls to macOS) | Same crosvm patterns; Rust code to start from | WHPX is x86 only, fundamentally different exit model; more confusing than helpful | Rejected |
| C: Use QEMU+HVF instead of porting crosvm | Zero implementation work | Two VMMs to maintain; no gfxstream in QEMU's HVF path; defeats unified architecture | Rejected |

## Phases

### Phase 1: FFI Bindings and Build Infrastructure

**Objective**: Create Rust FFI bindings for Apple Hypervisor.framework C API and configure the crosvm build system for macOS.

**Expected Results**:
- [ ] `hypervisor/src/hvf/ffi.rs` exists with complete Rust bindings for HVF C API (hv_vm_create, hv_vcpu_create, hv_vcpu_run, hv_vm_map, hv_vcpu_get_reg, etc.)
- [ ] `hypervisor/Cargo.toml` has `hvf` feature flag and `[target.'cfg(target_os = "macos")'.dependencies]`
- [ ] `hypervisor/src/lib.rs` has conditional `pub mod hvf` import
- [ ] `HypervisorKind::Hvf` added to the enum
- [ ] Entitlements plist created for code signing
- [ ] `cargo build --features hvf` compiles on macOS (linking against Hypervisor.framework)

**Dependencies**: None

**Status**: COMPLETE

### Phase 2: Hypervisor + Vm Trait Implementation

**Objective**: Implement `Hypervisor` and `Vm` (+ `VmAArch64`) traits wrapping HVF VM lifecycle and memory management.

**Expected Results**:
- [ ] `hypervisor/src/hvf/mod.rs` implements `Hypervisor` trait (`Hvf` struct)
- [ ] `hypervisor/src/hvf/vm.rs` implements `Vm` and `VmAArch64` traits (`HvfVm` struct)
- [ ] VM creation: `hv_vm_create` → `HvfVm`
- [ ] Memory mapping: `add_memory_region` → `hv_vm_map`, `remove_memory_region` → `hv_vm_unmap`
- [ ] vCPU creation: `create_vcpu` → `hv_vcpu_create`
- [ ] ioevent registration: user-space ioevent dispatch (no kernel support, like WHPX)
- [ ] Unit test: create VM, map memory, destroy VM — passes

**Dependencies**: Phase 1

**Status**: COMPLETE

### Phase 3: Vcpu Trait Implementation — Run Loop and Exit Handling

**Objective**: Implement `Vcpu` and `VcpuAArch64` traits with the core run loop, translating HVF ARM64 exception exits to crosvm `VcpuExit` enum.

**Expected Results**:
- [ ] `hypervisor/src/hvf/vcpu.rs` implements `Vcpu` and `VcpuAArch64` traits (`HvfVcpu` struct)
- [ ] `run()` calls `hv_vcpu_run()` and translates exit reasons:
  - EC_DATAABORT → `VcpuExit::Mmio` (syndrome parsing: iswrite, sas, srt, ipa)
  - EC_SYSTEMREGISTERTRAP → `VcpuExit::MsrAccess`
  - EC_WFX_TRAP → `VcpuExit::Hlt`
  - EC_AA64_HVC → `VcpuExit::Hypercall`
  - EC_AA64_SMC → `VcpuExit::Hypercall`
  - EC_AA64_BKPT, EC_SOFTWARESTEP, EC_BREAKPOINT, EC_WATCHPOINT → `VcpuExit::Debug`
- [ ] `handle_mmio()` parses syndrome and calls handler with correct `IoParams`
- [ ] `set_one_reg` / `get_one_reg` wrap `hv_vcpu_get_reg` / `hv_vcpu_set_reg`
- [ ] `init()` configures VCPU features (PSCI, etc.)
- [ ] PC advance (+4) for trapped instructions
- [ ] Unit test: create VM, create vCPU, set registers, run simple instruction — passes

**Dependencies**: Phase 2

**Status**: COMPLETE

### Phase 4: macOS Platform Layer Completion

**Objective**: Implement the 33 `todo!()` stubs in `base/src/sys/macos/mod.rs` to make crosvm's base layer fully functional on macOS.

**Expected Results**:
- [ ] All `todo!()` in `base/src/sys/macos/mod.rs` replaced with working implementations
- [ ] `EventContext<T>` implemented using kqueue (epoll equivalent)
- [ ] `MemoryMapping` implemented using mmap/munmap
- [ ] `SharedMemory` implemented using shm_open
- [ ] `getpid`, `set_thread_name`, `SafeDescriptor::eq`, `ioctl` — implemented
- [ ] `file_punch_hole`, `file_write_zeroes_at` — implemented
- [ ] `cargo build` for crosvm main binary compiles on macOS

**Dependencies**: Phase 1

**Status**: COMPLETE

### Phase 5: Integration — Boot Linux Kernel

**Objective**: Assemble all pieces and boot the Aetheria ARM64 kernel using crosvm on macOS, getting serial console output.

**Expected Results**:
- [ ] `crosvm run` on macOS with `--kernel vmlinux-arm64` boots the Aetheria kernel
- [ ] Serial console output visible (kernel boot log)
- [ ] Kernel reaches init (panic is acceptable if no rootfs — proves kernel+VM works)
- [ ] No crashes or hangs in the crosvm process

**Dependencies**: Phase 2, Phase 3, Phase 4

**Status**: PENDING

## Findings

### F-001: Trait Surface Is Larger Than Initially Estimated

The feasibility study estimated ~2000-2800 lines. Actual trait surface includes not only `Hypervisor`, `Vm`, `Vcpu` but also `VmAArch64` and `VcpuAArch64` with ~20 additional ARM64-specific methods (register access, PSCI, PMU, pvtime, snapshots, cache info, debug, FDT generation). This may push the estimate higher.

Source: `hypervisor/src/aarch64.rs` (291 lines of trait definitions)

### F-002: Some Vm Trait Methods Are Linux-Only

Methods like `madvise_pageout_memory_region` and `madvise_remove_memory_region` are gated with `#[cfg(any(target_os = "android", target_os = "linux"))]` and don't need macOS implementation.

Source: `hypervisor/src/lib.rs` lines 186-203

### F-003: VcpuSignalHandle Is Linux-Only

`VcpuSignalHandle` and `signal_handle()` are gated with `#[cfg(any(target_os = "android", target_os = "linux"))]`. macOS HVF backend does not need to implement signal-based exit; `set_immediate_exit` via `hv_vcpu_cancel` is sufficient.

Source: `hypervisor/src/lib.rs` lines 352-380, 404
