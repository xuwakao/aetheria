# ISS-010: PSCI emulation hardcoded in vCPU loop instead of proper device

Created: 2026-03-31
Status: RESOLVED
Severity: MEDIUM
Source: Full codebase review (2026-03-31)

## Description

PSCI (Power State Coordination Interface) handling is implemented directly in the `vcpu_loop` function in `src/crosvm/sys/macos.rs` (lines ~527-580) as a large match block inside the `VcpuExit::Hypercall` handler:

```rust
VcpuExit::Hypercall => {
    if let Err(e) = vcpu.handle_hypercall(&mut |abi| {
        let fid = abi.hypercall_id();
        match fid {
            0x84000000 => { /* PSCI_VERSION */ }
            0x84000006 => { /* PSCI_MIGRATE_INFO_TYPE */ }
            0x8400000a => { /* PSCI_FEATURES */ }
            0x84000002 => { /* PSCI_CPU_OFF */ }
            0xc4000003 => { /* PSCI_CPU_ON */ }
            0x84000008 => { /* PSCI_SYSTEM_OFF */ }
            0x84000009 => { /* PSCI_SYSTEM_RESET */ }
            _ => hypercall_bus.handle_hypercall(abi)
        }
    })
}
```

## Issues

1. **Should be on the hypercall_bus**: crosvm's architecture routes PSCI through the `hypercall_bus` via registered `SmcccTrng` or similar bus devices. `build_vm` in `aarch64/src/lib.rs` does register PSCI-related handlers on the hypercall_bus, but our macOS code intercepts them first.

2. **PSCI_CPU_ON returns success but doesn't start secondary CPUs**: The handler returns 0 (success) for CPU_ON but never actually creates a new vCPU. This silently lies to the kernel.

3. **PSCI_SYSTEM_OFF/RESET don't actually exit the VM**: They set results but the vCPU loop continues. Should trigger ExitState::Stop or ExitState::Reset.

4. **Magic PSCI function IDs**: Should use named constants from the hypervisor crate.

## Recommended Fix

1. Remove PSCI handling from vcpu_loop
2. Register a proper PSCI device on the hypercall_bus that handles PSCI calls
3. For SYSTEM_OFF/RESET, send a message via vm_evt_wrtube to signal the control loop
4. For CPU_ON, return PSCI_NOT_SUPPORTED until multi-vCPU is implemented
5. Use named constants from `hypervisor::PSCI_*`

## Resolution

Resolved by creating `devices/src/psci.rs` (PsciDevice) implementing BusDevice/BusDeviceSync.
- Registered on hypercall_bus for PSCI 32-bit and 64-bit FID ranges
- SYSTEM_OFF/RESET signal via `Arc<AtomicU8>` checked by vcpu_loop
- CPU_ON honestly returns PSCI_NOT_SUPPORTED (-1) instead of lying with SUCCESS
- Named constants for all PSCI function IDs and return values
- vcpu_loop now delegates all hypercalls to the bus (same pattern as Linux/KVM)

## Findings
