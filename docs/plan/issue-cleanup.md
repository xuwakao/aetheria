# Plan: issue-cleanup

Created: 2026-03-31T14:00:00+08:00
Status: COMPLETED
Source: Full codebase review ISS-001 through ISS-016

## Task Description

Resolve all 16 issues identified in the codebase review. Grouped by type and dependency order.

## Phases

### Phase 1: Debug logging cleanup (ISS-001, ISS-002, ISS-014)

**Objective**: Remove all development debug logging and hardcoded debug paths.

**Expected Results**:
- [ ] All MMIO read/write trace logging removed from vCPU loop
- [ ] All exit counter logging removed or changed to `debug!()`
- [ ] IRQ event fire logging removed
- [ ] service_irq_event logging removed
- [ ] Hardcoded `/tmp/crosvm_fdt.dtb` replaced with `cfg.dump_device_tree_blob`
- [ ] Duplicate earlycon cmdline entry removed
- [ ] `cargo build --no-default-features` compiles
- [ ] Boot test: kernel + initramfs still produces serial output

**Dependencies**: None
**Status**: PENDING

### Phase 2: Hardcoded parameters cleanup (ISS-003, ISS-006, ISS-013)

**Objective**: Remove hardcoded kernel params, add named constants, eliminate duplication.

**Expected Results**:
- [ ] Remove `panic=30`, `keep_bootcon`, `rdinit=/init` from extra_kernel_params
- [ ] Keep only earlycon from `serial_parameters.earlycon` (already set at line 215)
- [ ] Define `HV_BAD_ARGUMENT`, `GIC_SPI_BASE`, `MPIDR_RES1` constants
- [ ] GIC addresses imported from single source (aarch64 crate constants)
- [ ] GICD_CTLR register value documented with comment
- [ ] `cargo build --no-default-features` compiles
- [ ] Boot test passes

**Dependencies**: Phase 1
**Status**: PENDING

### Phase 3: Code quality fixes (ISS-007, ISS-012)

**Objective**: Document SOCK_SEQPACKET change, fix GIC config memory leak.

**Expected Results**:
- [ ] `base/src/sys/macos/net.rs` has detailed comment explaining SOCK_STREAM substitution
- [ ] `hypervisor/src/hvf/vm.rs` releases hv_gic_config_t after hv_gic_create
- [ ] `cargo build --no-default-features` compiles

**Dependencies**: None
**Status**: PENDING

### Phase 4: PSCI proper implementation (ISS-010)

**Objective**: Move PSCI emulation from vcpu_loop into a proper hypercall bus device.

**Expected Results**:
- [ ] PSCI handling removed from vcpu_loop match block
- [ ] Hypercall bus routes PSCI calls to kernel-registered handler (or new PSCI device)
- [ ] PSCI_SYSTEM_OFF/RESET trigger ExitState change
- [ ] PSCI_CPU_ON returns PSCI_NOT_SUPPORTED (honest)
- [ ] Boot test: PSCI still probed successfully by kernel

**Dependencies**: Phase 2
**Status**: COMPLETE

### Phase 5: Document accepted limitations (ISS-005, ISS-008, ISS-015, ISS-016)

**Objective**: Mark issues that are intentional limitations with clear documentation.

**Expected Results**:
- [ ] ISS-005: macOS 14 limitation documented in code comment + issue marked WONTFIX
- [ ] ISS-008: Debug stubs documented with TODO comments + issue marked DEFERRED
- [ ] ISS-015: Simplified guest memory documented + issue marked DEFERRED
- [ ] ISS-016: Feature-gated stubs documented + issue marked DEFERRED
- [ ] All 4 issue files updated with resolution status

**Dependencies**: None
**Status**: PENDING

### Phase 6: Interactive console (ISS-009)

**Objective**: Connect host stdin to VM serial input for interactive shell.

**Expected Results**:
- [ ] Stdin reader thread spawned before vCPU loop
- [ ] Host keyboard input delivered to Serial device's input channel
- [ ] initramfs updated with `exec /bin/sh` (interactive shell)
- [ ] User can type commands and see output
- [ ] Ctrl+C in host terminal stops the VM

**Dependencies**: Phase 1 (debug logging removed so output is clean)
**Status**: PENDING

### Phase 7: Document remaining architecture issues (ISS-004, ISS-011)

**Objective**: Document single-vCPU and no-control-socket as known architectural limitations with proposed solutions.

**Expected Results**:
- [ ] ISS-004: Updated with detailed analysis + marked DEFERRED with solution path
- [ ] ISS-011: Updated with dependency on ISS-004 + marked DEFERRED
- [ ] Both issues reference the recommended solution (Option A from ISS-004)

**Dependencies**: None
**Status**: COMPLETE

## Findings
