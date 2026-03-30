# ISS-009: Serial stdin not connected — no interactive console

Created: 2026-03-31
Status: OPEN — highest priority remaining issue
Severity: HIGH
Source: Full codebase review (2026-03-31)

## Description

crosvm's serial device is configured with `SerialType::Stdout` and `stdin: true` in the serial parameters, but the vCPU loop does not integrate host stdin with the VM's serial input. The serial device receives input via its `in_channel`, but no mechanism feeds host keyboard input into it.

On Linux crosvm, the main event loop (`run_control`) monitors stdin via `WaitContext` and feeds characters to the serial device's input channel. On macOS, our simplified `vcpu_loop` runs directly on the main thread and has no stdin polling.

## Impact

- Cannot type commands into the VM
- Cannot run interactive shell (`/bin/sh`)
- VM is output-only — useful for batch scripts but not interactive use

## Root Cause

The macOS `run_config` does not implement the control event loop that Linux has. The vCPU runs directly on the main thread, which blocks stdin polling.

## Relationship

Related to ISS-004 (single vCPU main thread). Fixing ISS-004 to free the main thread would enable a proper control event loop that includes stdin handling.

## Recommended Fix

### Short-term: Spawn stdin reader thread
- Before entering vcpu_loop, spawn a thread that reads from host stdin
- Feed characters to the Serial device's input channel via Tube or crossbeam channel
- This works even with the main thread running the vCPU

### Long-term: Implement full control event loop
- Move vCPU to a separate thread (requires ISS-004 fix)
- Main thread runs WaitContext polling stdin, control socket, IRQ events, VM events

## Findings
