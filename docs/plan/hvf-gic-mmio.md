# Plan: hvf-gic-mmio

Created: 2026-03-30T22:00:00+08:00
Status: PAUSED
Source: Boot log shows `GICv3: no distributor detected` and `arch_timer: No interrupt available`. [plan/crosvm-hvf-runtime#Phase3]

## Task Description

Implement GICv3 distributor (GICD) and redistributor (GICR) MMIO register emulation so the Linux kernel can detect and initialize the GIC, enabling interrupts and the arch timer.

## Boot Log Evidence

```
GICv3: /intc: no distributor detected, giving up
arch_timer: No interrupt available, giving up
```

The kernel reads GICD_PIDR2 at 0x3fffffe8 → returns 0x0 → concludes no GIC exists.

## Alternatives

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| A: MMIO bus device emulating GICD/GICR registers | Clean, modular, follows crosvm patterns | ~300-500 lines | **Selected** |
| B: Handle in vCPU MMIO handler directly | Simple | Mixes concerns, hard to maintain | Rejected |

## Phases

### Phase 1: GIC Distributor (GICD) MMIO emulation

**Objective**: Create a BusDevice that responds to GICD MMIO reads at the GIC distributor address range (0x3FFF0000-0x3FFFFFFF). Returns correct identification registers so the kernel detects GICv3.

**Expected Results** (real implementation):
- [ ] New file `devices/src/irqchip/hvf_gic_mmio.rs` with `GicDistributor` struct implementing `BusDevice`
- [ ] GICD_CTLR (offset 0x0000): read returns 0x0 (disabled), write stores enable bits
- [ ] GICD_TYPER (offset 0x0004): read returns IT_lines=1 (64 IRQs), CPUNumber=0, SecurityExtn=0
- [ ] GICD_IIDR (offset 0x0008): read returns implementor ID
- [ ] GICD_PIDR2 (offset 0xFFE8): read returns 0x3B (GICv3 architecture revision)
- [ ] All other GICD offsets: read returns 0, write is no-op
- [ ] Device registered on MMIO bus at AARCH64_GIC_DIST_BASE with AARCH64_GIC_DIST_SIZE
- [ ] Kernel boot log shows `GICv3: ... detected` instead of `no distributor detected`
- [ ] `cargo build --no-default-features` compiles

**Dependencies**: None
**Risks**: Wrong PIDR2 value will cause kernel to reject the GIC

### Phase 2: GIC Redistributor (GICR) MMIO emulation

**Objective**: Add GICR MMIO emulation so the kernel can initialize per-CPU interrupt state.

**Expected Results** (real implementation):
- [ ] `GicRedistributor` struct implementing `BusDevice` in same file
- [ ] GICR_CTLR, GICR_TYPER, GICR_PIDR2 return correct values
- [ ] GICR_WAKER returns 0 (processor is awake)
- [ ] Registered at redistributor base address (below distributor)
- [ ] Kernel initializes GIC without errors
- [ ] `arch_timer` initializes (timer interrupts routed through GIC)
- [ ] Boot log timestamps progress beyond 0.000000

**Dependencies**: Phase 1
**Risks**: Redistributor layout must match FDT reg property exactly

## Findings
