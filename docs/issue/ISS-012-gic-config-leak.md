# ISS-012: hv_gic_config_t object never released — potential memory leak

Created: 2026-03-31
Status: OPEN
Severity: LOW
Source: Full codebase review (2026-03-31)

## Description

In `hypervisor/src/hvf/vm.rs`, the `hv_gic_config_t` object created by `hv_gic_config_create()` is never released. The Apple Hypervisor.framework uses Objective-C ARC (Automatic Reference Counting) for these objects, declared with `OS_OBJECT_RETURNS_RETAINED`.

```rust
let config = unsafe { ffi::hv_gic_config_create() };
// ... configure ...
let ret = unsafe { ffi::hv_gic_create(config) };
// config is never released
```

## Impact

Minor memory leak of one config object per VM creation. Since VMs are typically long-lived and created once, the practical impact is negligible.

## Recommended Fix

Call `os_release(config)` after `hv_gic_create` completes. In Rust, this requires:

```rust
extern "C" { fn os_release(object: *mut std::ffi::c_void); }
unsafe { os_release(config as *mut _); }
```

Or wrap in a RAII type that calls `os_release` on Drop.

## Findings
