# ISS-013: GIC address constants duplicated across three files

Created: 2026-03-31
Status: RESOLVED
Severity: LOW
Source: Full codebase review (2026-03-31)

## Description

GIC distributor and redistributor base addresses are defined in three places:

1. `aarch64/src/lib.rs` — Canonical source:
   ```rust
   const AARCH64_GIC_DIST_BASE: u64 = 0x40000000 - AARCH64_GIC_DIST_SIZE;  // 0x3FFF0000
   ```

2. `hypervisor/src/hvf/vm.rs` — Duplicated:
   ```rust
   const GIC_DIST_BASE: u64 = 0x3FFF0000;
   ```

3. `src/crosvm/sys/macos.rs` — Duplicated again:
   ```rust
   const GIC_DIST_BASE: u64 = 0x3FFF0000;
   ```

If any of these are changed independently, the FDT, HVF GIC, and MMIO emulation will disagree on addresses, causing boot failure.

## Recommended Fix

Export the constants from `aarch64` crate and import in `hypervisor` and `src/crosvm`. The `aarch64` crate already defines them as `pub(crate)` — change to `pub` and import.

Alternatively, pass GIC addresses as parameters from `run_config` to `HvfVm::new()` instead of hardcoding.

## Findings
