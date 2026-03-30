# ISS-005: macOS 14 GIC fallback does not deliver interrupts

Created: 2026-03-31
Status: DEFERRED — documented limitation
Severity: MEDIUM
Source: Full codebase review (2026-03-31)

## Description

On macOS 14 (where `hv_gic_*` APIs are unavailable), the fallback path uses:
1. `hvf_gic_mmio.rs` — Software GICD/GICR MMIO register emulation
2. `hv_vcpu_set_pending_interrupt(IRQ, true)` — Physical IRQ line assertion

This combination does NOT actually deliver interrupts to the guest because:
- `hv_vcpu_set_pending_interrupt` sets the physical IRQ line but HVF's internal GIC (without native API) returns spurious (1023) for `ICC_IAR1_EL1` reads
- The kernel's GIC handler reads `ICC_IAR1_EL1`, gets 1023, and ignores the interrupt
- Timer interrupts (PPI 27) are never delivered
- Serial interrupts (SPI 32) are never delivered

## Affected Files

| File | Role |
|------|------|
| `devices/src/irqchip/hvf_gic_mmio.rs` | GICD/GICR MMIO emulation (skeleton) |
| `devices/src/irqchip/hvf_gic.rs` lines 185-197 | Fallback IRQ injection via `hv_vcpu_set_pending_interrupt` |
| `hypervisor/src/hvf/vcpu.rs` lines 333-340 | Fallback vtimer handler |
| `hypervisor/src/hvf/vcpu.rs` lines 152-180 | Sysreg trap handler for ICC registers |

## Evidence

When native GIC is not available:
- `/proc/interrupts` shows 0 for all interrupt sources
- Kernel boot timestamps are all `[0.000000]` (no timer)
- Userspace tty writes never flush (no serial TX interrupt)
- Kernel prints `GICv3: no distributor detected` (MMIO emulation was active before native GIC)

With native GIC (macOS 15):
- `/proc/interrupts` shows 34+ timer interrupts, 4+ serial interrupts
- Boot timestamps progress normally
- Userspace tty writes flush correctly

## Root Cause

On macOS 14, HVF does not expose GIC virtual interrupt injection APIs (`hv_gic_set_spi`, etc.). The only available mechanism is `hv_vcpu_set_pending_interrupt(IRQ/FIQ, true/false)` which sets the physical interrupt line. However, HVF still handles `ICC_IAR1_EL1` internally and returns a spurious interrupt ID because there's no way to populate the GIC's list registers from userspace.

## Possible Solutions

### A: Accept macOS 14 as unsupported for full interrupt delivery
- Document that macOS 15+ is required for full VM functionality
- macOS 14 can still boot kernel with earlycon output (printk works via direct THR writes)
- macOS 14 cannot run userspace programs that depend on interrupts

### B: Implement full userspace GIC with list register emulation
- Emulate ICC_IAR1_EL1/ICC_EOIR1_EL1 in the sysreg trap handler
- Maintain a software list register to track pending interrupt IDs
- Return correct INTID from ICC_IAR1_EL1 when IRQ is pending
- Complexity: ~500 lines, requires careful interrupt priority/preemption handling
- Risk: macOS 14's ICC register trapping behavior may not be consistent

### C: Use polling-mode serial driver
- Pass `8250.nr_uarts=0` to disable interrupt-driven 8250
- Use earlycon for all serial output (polling mode)
- Limitation: userspace programs cannot write to /dev/ttyS0

## Recommended Fix

Option A (document limitation) for now. macOS 14 becomes increasingly irrelevant as macOS 15 adoption grows. The MMIO emulation (`hvf_gic_mmio.rs`) should be kept for kernel GIC detection but documented as non-functional for interrupt delivery.

## Findings

### F-001: HVF ICC register handling is opaque on macOS 14
On macOS 14, ICC system registers (ICC_IAR1_EL1, ICC_EOIR1_EL1, etc.) are handled by HVF internally but without proper virtual interrupt injection support. The VMM cannot influence what ICC_IAR1_EL1 returns.

### F-002: macOS 15 hv_gic_* APIs are the proper solution
Apple designed the `hv_gic_create/hv_gic_set_spi` APIs specifically to solve this problem. The native GIC handles all distributor, redistributor, ICC, and list register emulation in hardware/firmware.
