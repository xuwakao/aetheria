# ISS-008: HvfVcpu debug register stubs and incomplete VcpuAArch64 methods

Created: 2026-03-31
Status: DEFERRED — implemented when needed
Severity: LOW
Source: Full codebase review (2026-03-31)

## Description

`hypervisor/src/hvf/vcpu.rs` has several incomplete method implementations in the `VcpuAArch64` trait:

### 1. `set_guest_debug` — returns ENOTSUP
```rust
fn set_guest_debug(&self, ...) -> Result<()> {
    Err(Error::new(libc::ENOTSUP))
}
```
GDB debugging of the guest is not supported. HVF provides `hv_vcpu_set_trap_debug_exceptions` which could enable this.

### 2. `get_max_hw_bps` — returns 0
```rust
fn get_max_hw_bps(&self) -> Result<usize> {
    Ok(0)  // TODO: query HVF for hardware breakpoint count
}
```
Should query the actual hardware breakpoint count from the CPU's ID_AA64DFR0_EL1 register.

### 3. `get_system_regs` — returns empty BTreeMap
```rust
fn get_system_regs(&self) -> Result<BTreeMap<AArch64SysRegId, u64>> {
    Ok(BTreeMap::new())
}
```
Used for snapshotting. Should iterate known system registers and read their values.

### 4. `set_system_regs` — no-op
```rust
fn set_system_regs(&self, _regs: &BTreeMap<AArch64SysRegId, u64>) -> Result<()> {
    Ok(())
}
```
Used for restore from snapshot. Should write system register values.

## Impact

- GDB debugging of guest code is not available
- VM snapshots cannot save/restore system register state
- Hardware breakpoints report 0 available (GDB will use software breakpoints only)

## Recommended Fix

These are acceptable stubs for initial implementation. They should be tracked and implemented when:
1. GDB support is needed (set_guest_debug + get_max_hw_bps)
2. Snapshot/restore is needed (get_system_regs + set_system_regs)

No immediate action required.

## Findings
