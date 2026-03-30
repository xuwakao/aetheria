# ISS-002: Hardcoded FDT dump path in run_config

Created: 2026-03-31
Status: RESOLVED
Severity: HIGH
Source: Full codebase review (2026-03-31)

## Description

`src/crosvm/sys/macos.rs` line 255 passes a hardcoded `/tmp/crosvm_fdt.dtb` path to `Arch::build_vm` as the `dump_device_tree_blob` parameter. This was added during debugging to inspect the FDT and should be removed or made configurable via command-line flag.

## Affected File

`src/crosvm/sys/macos.rs:255`
```rust
Some(std::path::PathBuf::from("/tmp/crosvm_fdt.dtb")), // dump FDT for debugging
```

## Impact

- Writes a file to `/tmp/` on every VM start without user consent
- Overwrites any existing file at that path
- No cleanup on exit

## Recommended Fix

Change to `None` (default, no dump), or pass through from `cfg.dump_device_tree_blob` which is the standard crosvm command-line option:

```rust
cfg.dump_device_tree_blob.clone(), // user-controlled via --dump-device-tree-blob
```

## Findings
