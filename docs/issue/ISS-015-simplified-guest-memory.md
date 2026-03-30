# ISS-015: Simplified create_guest_memory missing Linux features

Created: 2026-03-31
Status: OPEN
Severity: LOW
Source: Full codebase review (2026-03-31)

## Description

`src/crosvm/sys/macos.rs` `create_guest_memory` is a simplified version of Linux's implementation. Missing features:

| Feature | Linux | macOS | Impact |
|---------|-------|-------|--------|
| `file_backed_mappings_ram` | Supported — punches holes for file-backed regions | Not supported | Cannot use `--file-backed-mapping` flag |
| `hugepages` | `MemoryPolicy::USE_HUGEPAGES` | Ignored | No huge page optimization |
| `lock_guest_memory` | `MemoryPolicy::LOCK_GUEST_MEMORY` | Ignored | Memory can be swapped out |
| `USE_PUNCHHOLE_LOCKED` | When no sandbox | Not set | Balloon may not work correctly |
| `unmap_guest_memory_on_fork` | `use_dontfork()` | Not called | No protected VM fork safety |

## Impact

All missing features are advanced optimizations or edge cases. Basic VM operation is not affected. These become relevant when:
- Running with `--file-backed-mapping` (ISS)
- Running with `--hugepages` (performance)
- Running with balloon device (memory management)
- Running protected VMs (security)

## Recommended Fix

Implement each feature when the corresponding use case is needed. The current simplified version is correct for basic operation.

## Findings
