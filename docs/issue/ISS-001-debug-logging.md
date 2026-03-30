# ISS-001: Production-blocking debug logging in vCPU loop and IRQ handler

Created: 2026-03-31
Status: RESOLVED
Severity: CRITICAL
Source: Full codebase review (2026-03-31)

## Description

Multiple files contain verbose debug logging that was added during development and never removed. This logging fires on every MMIO operation, every vCPU exit, and every IRQ event — destroying performance and flooding console output in production.

## Affected Files

### 1. `src/crosvm/sys/macos.rs` — vCPU loop (CRITICAL)

| Lines | Issue | Impact |
|-------|-------|--------|
| 516-521 | Every vCPU exit logs exit counters + PC address | Fires 100+ times per boot, thousands during runtime |
| 541-555 | Every MMIO read in serial range (0x3f8-0x400) logged with data | Fires on every serial register access |
| 551-563 | Every MMIO write logged; serial TX decoded as individual characters | Fires on every byte output to serial |
| 587-589 | Every Intr exit logs count + PC | Fires on every timer/interrupt |

**Evidence**: In a 10-second boot, these produce ~10,000 log lines to stderr.

### 2. `src/crosvm/sys/macos.rs` — IRQ handler thread

| Lines | Issue | Impact |
|-------|-------|--------|
| 477 | `info!("IRQ event {} fired!", index)` on every IRQ event | Fires 4+ times per serial write cycle |

### 3. `devices/src/irqchip/hvf_gic.rs`

| Lines | Issue | Impact |
|-------|-------|--------|
| 159 | `base::info!("service_irq_event: index={} gsi={} source={}", ...)` | Fires on every device interrupt |
| 178 | `base::info!("hv_gic_set_spi({}) OK", intid)` removed but deassert not logged | Minor |

### 4. `hypervisor/src/hvf/vm.rs`

| Lines | Issue | Impact |
|-------|-------|--------|
| 57-66 | Every guest memory region mapping logged with host/guest addresses | Fires once per region at startup — acceptable as info |
| 113-116 | GIC parameter query results logged | Fires once at startup — acceptable |
| 229-237 | Every `add_memory_region` call logged | Fires per PCI BAR setup |

### 5. `hypervisor/src/hvf/vcpu.rs`

| Lines | Issue | Impact |
|-------|-------|--------|
| 124-127 | ICC/GIC system register MRS traps logged | Only fires when native GIC unavailable — acceptable |
| 140-143 | ICC/GIC system register MSR traps logged | Same condition — acceptable |

## Recommended Fix

1. **Remove** all MMIO read/write trace logging from vCPU loop (lines 541-563)
2. **Remove** exit counter logging or change to `debug!()` with sampling (lines 516-521)
3. **Remove** IRQ event fire logging (line 477) or change to `debug!()`
4. **Remove** service_irq_event logging (hvf_gic.rs line 159) or change to `debug!()`
5. **Keep** startup-only logs (memory mapping, GIC creation) as `info!()`
6. **Keep** error-level logs for actual failures

## Findings
