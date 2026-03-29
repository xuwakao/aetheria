# Issue: crosvm-hvf

Source: [plan/crosvm-hvf#Phase1]

## ISS-001: cros_async Has No macOS Support — Blocks All Compilation

**Status**: IN-PROGRESS
**Severity**: Blocker
**Discovered**: 2026-03-30T00:35

### Symptom
`cargo build -p hypervisor --features hvf` on macOS fails with 39 errors in `cros_async` crate. Uses Linux-only APIs (io_uring, epoll) with no macOS fallback.

### Dependency Chain
`hypervisor` → `base` → `audio_streams` → `cros_async`

### Fix Approach Selected
Stub `cros_async` for macOS — add minimal `cfg(target_os = "macos")` module that compiles but provides no async I/O. The hypervisor crate does not use async I/O; `cros_async` is only transitively pulled in.

## Findings
