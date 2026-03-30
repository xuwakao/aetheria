# ISS-014: Duplicate earlycon kernel cmdline entry

Created: 2026-03-31
Status: OPEN
Severity: LOW
Source: Full codebase review (2026-03-31)

## Description

The kernel command line contains two earlycon entries:

```
earlycon=uart8250,mmio,0x3f8 earlycon=uart8250,mmio32,0x3f8
```

Source 1: `serial_parameters[COM1].earlycon = true` (line 215 of macos.rs) → `get_serial_cmdline` generates `earlycon=uart8250,mmio,0x3f8`

Source 2: `extra_kernel_params.push("earlycon=uart8250,mmio32,0x3f8")` (line 129 of macos.rs) — hardcoded duplicate with `mmio32` variant

The `mmio32` variant was added during debugging when `mmio` wasn't working (before CONFIG_SERIAL_8250 was enabled in the kernel). Now both are unnecessary.

## Impact

- Wastes kernel cmdline space (limited to 2048 bytes on ARM64)
- May cause confusion in kernel log: `earlycon: uart8250 at MMIO 0x3f8 (options '')`
- No functional harm — kernel uses the first matching earlycon

## Recommended Fix

Remove the hardcoded `earlycon=uart8250,mmio32,0x3f8` from extra_kernel_params. The `serial_parameters.earlycon = true` path generates the correct entry via `get_serial_cmdline`.

Related to ISS-003 (hardcoded kernel params).

## Findings
