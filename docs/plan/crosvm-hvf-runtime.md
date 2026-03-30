# Plan: crosvm-hvf-runtime

Created: 2026-03-30T18:00:00+08:00
Updated: 2026-03-30T19:40:00+08:00
Status: PAUSED
Source: [plan/crosvm-hvf#Phase5], [plan/crosvm-macos-port] (COMPLETED), [plan/crosvm-macos-real-impl] (COMPLETED)

## Task Description

Implement the runtime execution path for crosvm on macOS HVF: wire up `run_config` to create an HVF VM, set up devices, and boot a Linux kernel with serial console output.

## Key Insight: Minimal GIC

Analysis of the IrqChip trait shows the GIC implementation is simpler than initially estimated. HVF provides `hv_vcpu_set_pending_interrupt(vcpu, IRQ, pending)` for interrupt injection — we do NOT need full GICD/GICR register emulation. The kernel handles GIC system registers via traps. We only need:
1. Track IRQ → eventfd mappings (from `register_edge_irq_event`)
2. Inject pending IRQs before each vCPU run via `hv_vcpu_set_pending_interrupt`
3. Implement ~5 methods fully, stub ~15 as no-ops

## Alternatives

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| A: Minimal HvfGicChip (event routing + injection only) | ~200 lines, fast to implement, sufficient for serial boot | No full GICv3 register emulation | **Selected** — sufficient for boot verification |
| B: Full userspace GICv3 (GICD + GICR register emulation) | Correct for all guest OSes | ~1000+ lines, complex, may not be needed if kernel handles traps | Rejected for now — can upgrade later |
| C: Port QEMU arm_gicv3 | Proven | 3000+ lines C, licensing issues | Rejected |

## Phases

### Phase 1: HvfGicChip — Minimal IRQ chip

**Objective**: Implement `HvfGicChip` satisfying IrqChip + IrqChipAArch64 traits with event-based interrupt routing.

**Expected Results** (real implementation unless marked as planned stub):
- [ ] `devices/src/irqchip/hvf_gic.rs` exists with `HvfGicChip` struct
- [ ] IrqChip trait fully implemented: `add_vcpu`, `register_edge_irq_event`, `inject_interrupts` are real; remaining ~12 methods are documented no-ops
- [ ] IrqChipAArch64 trait: `get_vgic_version` returns ArmVgicV3, `has_vgic_its` returns false, `finalize` is no-op
- [ ] `inject_interrupts` calls `hv_vcpu_set_pending_interrupt` for pending IRQs
- [ ] `cargo build -p devices --no-default-features` compiles
- [ ] Unit test: register IRQ event, signal it, verify pending state

**Dependencies**: None
**Risks**:
- `hv_vcpu_set_pending_interrupt` only takes IRQ/FIQ type, not IRQ number — may need to track which IRQ is being serviced
- Edge vs level triggered semantics need care
**Status**: COMPLETE

### Phase 2: run_config — Wire up HVF VM execution

**Objective**: Implement `run_config` in `src/crosvm/sys/macos.rs` to create Hvf VM, set up memory, IRQ chip, and call the generic `run_vm`.

**Expected Results** (real implementation):
- [ ] `run_config` creates Hvf hypervisor, HvfVm with guest memory, HvfGicChip
- [ ] `setup_vm_components` adapted for macOS (portable fields only)
- [ ] `create_guest_memory` reused (calls Arch::guest_memory_layout)
- [ ] `run_vm` generic function called with HvfVcpu/HvfVm types
- [ ] `cargo build --no-default-features` compiles
- [ ] `crosvm run --kernel /dev/null` starts and exits with "failed to load kernel" (not crash/panic)

**Dependencies**: Phase 1
**Risks**:
- `run_vm` is 2000+ lines with many Linux-specific paths; may need additional cfg gates
- `setup_vm_components` references Linux-only VmComponents fields
**Status**: COMPLETE

### Phase 3: Boot verification

**Objective**: Boot the Aetheria ARM64 kernel using `crosvm run --kernel` and see serial output.

**Expected Results**:
- [ ] `crosvm run --kernel vmlinux-arm64` starts vCPU execution
- [ ] Serial console shows at least "Booting Linux" or early kernel messages
- [ ] Process exits cleanly (kernel panic without rootfs is acceptable)

**Dependencies**: Phase 2, a compiled ARM64 Linux kernel
**Risks**:
- HIGH: FDT generation may have issues (GIC addresses, serial address, memory layout)
- Timer interrupts may not work without vtimer configuration
- MMIO handling in vCPU run loop may miss devices

## Findings
