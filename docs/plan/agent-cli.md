# Plan: aetheria-agent + CLI (路线 A 产品化)

Status: COMPLETED
Created: 2026-04-01
Source: Architecture — host↔guest control plane for container management

## Objective

Build the agent (runs in guest VM) and CLI (runs on macOS host) that communicate
via vsock. This enables `aetheria exec <command>` to run commands inside the VM,
which is the foundation for container management.

## Architecture

```
macOS Host                              Linux Guest (VM)
┌─────────────────┐   Unix socket     ┌────────────────────┐
│ aetheria CLI    │◄──────────────────┤ aetheria-agent      │
│ (Go binary)     │ /tmp/aetheria-    │ (Go binary, PID 1   │
│                 │  vsock-3/port-1024│  or systemd service) │
│ Commands:       │                   │                      │
│  exec <cmd>     │                   │ Listens on AF_VSOCK  │
│  ping           │                   │ CID=2, port=1024     │
│  info           │                   │                      │
└─────────────────┘                   │ Executes commands    │
         │                            │ Returns stdout/stderr│
         │ starts crosvm              └────────────────────┘
         ▼
┌─────────────────┐
│ crosvm          │
│ virtio-vsock    │◄── forwards vsock to Unix socket
└─────────────────┘
```

**Communication flow** (phone-home pattern):
1. Host: CLI/daemon listens on Unix socket `/tmp/aetheria-vsock-3/port-1024`
2. VM boots, agent starts, connects to vsock CID=2 port=1024
3. crosvm vsock worker: OP_REQUEST → `UnixStream::connect("/tmp/.../port-1024")`
4. Bidirectional channel established
5. Host sends JSON commands, agent executes and returns results

## Alternatives

| Approach | Pros | Cons | Decision |
|----------|------|------|----------|
| gRPC over vsock | Typed API, codegen | Needs protobuf toolchain, heavy for MVP | Later |
| JSON-RPC over vsock | Simple, human-readable, no codegen | No streaming | **SELECTED for MVP** |
| Custom binary protocol | Fast | Hard to debug, version compat | Rejected |

## Phase 1: Agent — vsock echo server

**Objective**: Go binary that runs in guest, connects to host via vsock, echoes messages.

**Expected Results**:
1. `cmd/aetheria-agent/main.go` implements AF_VSOCK client connecting to CID=2 port=1024
2. Cross-compiles for linux/arm64: `GOOS=linux GOARCH=arm64 go build`
3. Binary included in rootfs, started by init
4. Agent connects, host can send text and receive echo
5. Verified via: host listens on Unix socket, sends "ping", receives "ping" back

**Dependencies**: None (vsock already working in crosvm)

## Phase 2: Agent — command execution

**Objective**: Agent executes commands and returns output over the vsock channel.

**Expected Results**:
1. JSON-RPC protocol: `{"method":"exec","params":{"cmd":"uname -a"}}`
2. Response: `{"result":{"stdout":"Linux...","stderr":"","exit_code":0}}`
3. Also supports: `ping`, `info` (returns hostname, uptime, memory)
4. Verified: host sends exec command, gets correct output

**Dependencies**: Phase 1

## Phase 3: CLI — host-side command tool

**Objective**: `aetheria` CLI that starts the VM, waits for agent connection, runs commands.

**Expected Results**:
1. `aetheria run` — starts crosvm, listens for agent, shows "VM ready"
2. `aetheria exec <cmd>` — sends command to agent, prints stdout
3. `aetheria ping` — health check
4. `aetheria stop` — sends shutdown command, waits for VM exit
5. Verified: `aetheria run` + `aetheria exec uname -a` in separate terminal

**Dependencies**: Phase 2

## Phase 4: Integration test

**Objective**: End-to-end: start VM → agent connects → run commands → stop VM.

**Expected Results**:
1. `aetheria run` boots VM in background
2. `aetheria exec whoami` returns "root"
3. `aetheria exec apk add curl` installs package
4. `aetheria exec curl https://example.com` fetches URL
5. `aetheria stop` cleanly shuts down VM

**Dependencies**: Phase 3
