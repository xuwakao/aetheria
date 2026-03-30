# ISS-011: No VM control socket — crosvm stop/suspend/resume inoperable

Created: 2026-03-31
Status: DEFERRED — architecture limitation, solution documented
Severity: MEDIUM
Source: Full codebase review (2026-03-31)

## Description

The macOS `run_config` does not create a control server socket or process VM control commands. The Linux implementation creates a `control_server_socket` that accepts connections from `crosvm stop`, `crosvm suspend`, `crosvm balloon`, etc. On macOS, none of these work.

## Impact

- `crosvm stop <socket>` — does not work (no socket created)
- `crosvm suspend` — does not work
- `crosvm resume` — does not work
- `crosvm balloon` — does not work
- `crosvm disk` — does not work
- Only way to stop VM is to kill the process (SIGKILL/SIGTERM)

## Root Cause

Same as ISS-004: the main thread runs the vCPU loop and cannot simultaneously poll a control socket. The Linux implementation runs vCPUs in threads and uses the main thread for the control event loop.

## Recommended Fix

Implement after ISS-004 is resolved. The control event loop should:
1. Create a Unix domain socket at the user-specified path
2. Poll the socket for incoming connections
3. Handle VmRequest messages (Stop, Suspend, Resume, etc.)
4. Signal vCPU threads to stop/suspend as needed

## Findings
