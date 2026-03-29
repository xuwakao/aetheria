# Plan: crosvm-hvf-runtime

Created: 2026-03-30T18:00:00+08:00
Status: PENDING
Source: [plan/crosvm-hvf#Phase5], [plan/crosvm-macos-port] (COMPLETED prerequisite)

## Task Description

Implement the runtime execution path for crosvm on macOS HVF: wire up `run_config` to create an HVF VM, set up devices, and boot a Linux kernel with serial console output.

## Key Blocker: Userspace GIC

All existing crosvm IRQ chip implementations (KvmKernelIrqChip, HallaKernelIrqChip, GeniezoneKernelIrqChip) delegate GIC emulation to the host kernel. Apple's Hypervisor.framework does NOT provide GIC emulation — the hypervisor only provides vCPU execution and memory mapping.

This means we must implement a **userspace GIC (vGIC)** that:
- Emulates GICv3 distributor (GICD) and redistributor (GICR) MMIO registers
- Handles interrupt routing, priority, enable/disable, pending/active states
- Injects virtual interrupts into vCPUs via `hv_vcpu_set_pending_interrupt`
- Implements the IrqChip and IrqChipAArch64 traits

Estimated complexity: ~800-1200 lines of Rust.

## Alternatives

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| A: Implement userspace GICv3 in crosvm | Full control, no external deps | ~1000 lines of complex register emulation | **Selected** — only viable option |
| B: Use Apple's built-in GIC (if any) | Zero implementation | Apple HVF does NOT expose GIC APIs | Rejected — not available |
| C: Port QEMU's GIC to Rust | Well-tested reference | 3000+ lines C, complex licensing | Rejected — too large, cleaner to write from spec |

## Phases

### Phase 1: Userspace GICv3 Implementation

**Objective**: Implement a userspace GICv3 that satisfies crosvm's IrqChip/IrqChipAArch64 traits.

**Expected Results**:
- [ ] `devices/src/irqchip/hvf_gic.rs` implements IrqChip + IrqChipAArch64
- [ ] GICD MMIO register emulation (CTLR, TYPER, ISENABLER, ICENABLER, ISPENDR, ICPENDR, IPRIORITYR, ITARGETSR, ICFGR, IROUTER)
- [ ] GICR MMIO register emulation (CTLR, WAKER, ISENABLER0, ICENABLER0, etc.)
- [ ] Interrupt injection via hv_vcpu_set_pending_interrupt
- [ ] Unit tests for basic interrupt routing

**Dependencies**: None
**Status**: PENDING

### Phase 2: run_config Implementation

**Objective**: Wire up run_config to create HVF VM, memory, IRQ chip, and call the generic run_vm.

**Expected Results**:
- [ ] `src/crosvm/sys/macos.rs` run_config creates Hvf, HvfVm, HvfGicChip
- [ ] setup_vm_components reused from Linux code (extracted to shared module or copied)
- [ ] create_guest_memory reused
- [ ] run_vm called with HvfVcpu/HvfVm types
- [ ] `crosvm run --kernel <path>` starts without crash

**Dependencies**: Phase 1
**Status**: PENDING

### Phase 3: Boot Verification

**Objective**: Boot the Aetheria ARM64 kernel and get serial console output.

**Expected Results**:
- [ ] `crosvm run --kernel vmlinux-arm64` boots
- [ ] Serial console shows kernel boot messages
- [ ] Kernel reaches init (panic acceptable without rootfs)

**Dependencies**: Phase 2
**Status**: PENDING

## Findings
