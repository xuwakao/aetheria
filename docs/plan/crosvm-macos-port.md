# Plan: crosvm-macos-port

Created: 2026-03-30T05:30:00+08:00
Status: COMPLETED
Source: Deep analysis of crosvm dependency chain. Supersedes [plan/crosvm-macos-build] (DEPRECATED).

## Task Description

Complete the macOS port of crosvm so `cargo build --no-default-features` produces a working binary on macOS ARM64. Based on analysis, exactly 7 sys modules are missing. Strategy: copy Linux modules (which are 96% POSIX-portable), adapt the few Linux-specific bits, add `run_hvf` to the main binary.

## Alternatives & Trade-offs

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| A: Copy Linux modules → adapt for macOS (bottom-up by crate layer) | Systematic, testable per-layer, leverages POSIX portability | Some modules are large (5552 lines for main) | **Selected** |
| B: Write macOS modules from scratch | Clean, no Linux baggage | Massive duplication, months of work | Rejected |
| C: Iterative compile-fix without analysis | Simple loop | Proved ineffective (previous attempt) — hidden deps cascade | Rejected (evidence: [plan/crosvm-macos-build]) |

## Phases

Phase structure follows the crate dependency graph bottom-up: leaf crates first, then crates that depend on them.

### Phase 1: Trivial modules (crosvm_cli, metrics_events)

**Objective**: Add empty macOS branches to crates that only have Windows-specific platform code (no Linux branch either — macOS needs nothing special).

**Expected Results**:
- [ ] `crosvm_cli/src/sys.rs` has `else if #[cfg(target_os = "macos")] {}` empty branch (real impl: no macOS-specific CLI needed)
- [ ] `metrics_events/src/sys.rs` has same empty branch (real impl: no macOS-specific metrics events)
- [ ] `cargo build -p crosvm_cli` compiles on macOS
- [ ] `cargo build -p metrics_events` compiles on macOS

**Dependencies**: None
**Status**: COMPLETE

### Phase 2: Leaf crates (arch, devices/sys, vm_control, disk fix)

**Objective**: Add macOS sys modules to crates that export platform-specific functions used by the main binary.

**Expected Results**:
- [ ] `arch/src/sys/macos.rs` exists — copy from Linux, adapt (301 lines, serial/pstore setup; real impl where POSIX, stub where Linux-only like pstore)
- [ ] `devices/src/sys/macos.rs` + sub-modules exist — acpi_event stub, serial_device (real impl: POSIX-portable)
- [ ] `vm_control/src/sys/macos.rs` exists — VmMemoryMapping (real impl), GPU display stubs
- [ ] `disk/src/sys/macos.rs` compilation issues fixed (FlockOperation, is_block_device_file)
- [ ] `cargo build -p arch`, `cargo build -p devices --no-default-features`, `cargo build -p vm_control --no-default-features`, `cargo build -p disk` all compile

**Dependencies**: Phase 1
**Risks**: `devices` has deep sub-module tree (virtio/vsock/sys, virtio/net/sys, serial/sys, console/sys). Each may need its own macOS module.
**Status**: COMPLETE

### Phase 3: Main binary platform modules (src/sys, src/crosvm/sys)

**Objective**: Add macOS platform modules for the crosvm main binary. This is the core — includes `run_hvf` function and the main entry point.

**Expected Results**:
- [ ] `src/sys/macos.rs` + `src/sys/macos/main.rs` exist — init_log, run_command, error_to_exit_code, cleanup (real impl: mostly generic, ~94 lines base)
- [ ] `src/crosvm/sys/macos.rs` exists with:
  - `run_hvf()` function (real impl: ~100 lines, creates Hvf/HvfVm, calls generic `run_vm`)
  - `run_config()` dispatch (real impl: routes to `run_hvf`)
  - `config.rs` (copy from Linux, ~1003 lines, mostly portable)
  - `cmdline.rs` (copy from Linux, ~137 lines, portable)
  - Device creation functions (copy from Linux, adapt Linux-only bits)
- [ ] `cargo build --no-default-features` for the full crosvm workspace compiles
- [ ] Output binary exists at `target/debug/crosvm`

**Dependencies**: Phase 2
**Risks**: The 5552-line `linux.rs` has complex device creation logic. Some device types may pull in more missing macOS modules (virtio subsystem). If this happens, document and fix iteratively.
**Status**: COMPLETE

### Phase 4: Code-sign and smoke test

**Objective**: Sign the binary with HVF entitlements and verify it starts without crashing.

**Expected Results**:
- [ ] `codesign --sign - --entitlements entitlements.plist target/debug/crosvm` succeeds
- [ ] `./target/debug/crosvm --help` prints help text without crash
- [ ] `./target/debug/crosvm run --no-default-features` with invalid args prints usage error (not crash)

**Dependencies**: Phase 3
**Status**: COMPLETE

## Findings
