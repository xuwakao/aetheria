# ISS-004: Single vCPU runs on main thread, blocking crosvm event loop

Created: 2026-03-31
Status: OPEN
Severity: MEDIUM
Source: Full codebase review (2026-03-31)

## Description

`src/crosvm/sys/macos.rs` runs the vCPU loop on the main thread:

```rust
let exit_state = if let Some(vcpu) = vcpus.first_mut() {
    match vcpu_loop(vcpu, &io_bus, &mmio_bus, &hypercall_bus, &irq_chip) {
```

This was necessary because:
1. HVF requires `hv_vcpu_run` to be called from the thread that created the vCPU
2. `Arch::build_vm` creates vCPUs on the main thread
3. HVF native GIC binds redistributors to vCPUs at creation time
4. Creating new vCPUs in separate threads breaks GIC binding

## Impact

1. **Main thread is blocked** — cannot run the crosvm control event loop (VM control socket, device hotplug, balloon, etc.)
2. **Only 1 vCPU supported** — multi-vCPU requires thread-per-vCPU
3. **No graceful shutdown** — IRQ handler thread Exit message is sent after main thread unblocks (after VM exits)
4. **No VM control** — `crosvm stop`, `crosvm suspend`, etc. cannot be processed

## Root Cause

HVF's thread-affinity requirement for vCPUs conflicts with crosvm's architecture where `Arch::build_vm` creates vCPUs on the caller's thread. On Linux/KVM, vCPUs can be moved between threads. On HVF, they cannot.

## Possible Solutions

### A: Modify build_vm to not create vCPUs (RECOMMENDED)
- Pass `vcpus: None` to RunnableLinuxVm
- Create vCPUs in dedicated threads after build_vm
- Set MPIDR before GIC is probed by kernel
- Requires GIC to be created before vCPUs but vCPU MPIDR set before GIC probe

### B: Create GIC after thread vCPUs are created
- build_vm creates main-thread vCPUs (for FDT)
- Thread vCPUs created with same MPIDR
- GIC created after thread vCPUs exist
- Problem: build_vm already generates FDT with GIC addresses

### C: Use a single thread for everything
- Current approach, extended to handle control events via non-blocking poll
- Main loop: check control events → run vCPU for a time slice → repeat
- Limited but functional for single-vCPU

### D: Move build_vm into the vCPU thread
- Restructure so build_vm runs in the vCPU thread
- Requires significant refactoring of run_config

## Recommended Fix

Option A is the cleanest long-term solution. Option C is the quickest interim fix.

## Findings

### F-001: HVF vCPU thread affinity is absolute
The Apple Hypervisor.framework documentation states: "vCPU's must be run on the thread they were created on." This is enforced at the API level — `hv_vcpu_run` returns `HV_ERROR` if called from a different thread.

### F-002: HVF native GIC binds to vCPU at creation
When `hv_gic_create` is called, it allocates redistributors based on the VM's max vCPU count. Each vCPU's MPIDR determines which redistributor it binds to. Destroying a vCPU invalidates its GIC binding.
