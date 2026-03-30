# ISS-006: Undocumented magic numbers and missing code comments

Created: 2026-03-31
Status: OPEN
Severity: LOW
Source: Full codebase review (2026-03-31)

## Description

Several files contain hardcoded numeric values without named constants or explanatory comments.

## Affected Locations

### 1. HVF error code `-85377021`
- `hypervisor/src/hvf/vm.rs:139` — `"hv_gic_create failed: {} (HV_BAD_ARGUMENT={})", ret, -85377021i32`
- `hypervisor/src/hvf/vcpu.rs` — Same pattern
- **Fix**: Define `const HV_BAD_ARGUMENT: hv_return_t = -85377021;` in `ffi.rs` (or compute from `0xfae94003`)

### 2. GICD_CTLR register value `0x2`
- `hypervisor/src/hvf/vm.rs:145` — `hv_gic_set_distributor_reg(0x0000, 0x2)`
- `0x0000` = GICD_CTLR offset, `0x2` = EnableGrp1NS bit
- **Fix**: Add comment `// GICD_CTLR (offset 0x0) = EnableGrp1NS (bit 1)` or define constants

### 3. GIC address constants duplicated
- `hypervisor/src/hvf/vm.rs` — `GIC_DIST_BASE: u64 = 0x3FFF0000`
- `src/crosvm/sys/macos.rs` — Same constants repeated
- `aarch64/src/lib.rs` — Canonical source `AARCH64_GIC_DIST_BASE`
- **Fix**: Import from aarch64 crate or define shared constants

### 4. SPI INTID offset `+ 32`
- `devices/src/irqchip/hvf_gic.rs:176` — `let intid = gsi + 32;`
- `32` is the SPI offset in GIC INTID space (SPIs start at INTID 32)
- **Fix**: Add `const GIC_SPI_BASE: u32 = 32;` or add comment

### 5. MPIDR RES1 bit
- `aarch64/src/lib.rs` — `let mpidr_val = (1u64 << 31) | (vcpu_id as u64);`
- Bit 31 is the MPIDR_EL1 RES1 bit per ARM Architecture Reference Manual
- **Fix**: Add comment or define `const MPIDR_RES1: u64 = 1 << 31;`

## Recommended Fix

Define named constants in appropriate locations. All fixes are cosmetic and do not affect functionality.

## Findings
