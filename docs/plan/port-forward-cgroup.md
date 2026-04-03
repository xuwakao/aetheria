# Plan: Port Forwarding & Cgroup Resource Isolation

Created: 2026-04-03T00:00:00Z
Status: COMPLETED
Source: User request — "完成端口转发和cgroup的资源隔离"

## Task Description

Implement two P0 features for the Aetheria container runtime:
1. **Port forwarding**: Map host (macOS) ports to container ports (`-p 8080:80`)
2. **Cgroups v2 resource isolation**: CPU, memory, and PID limits per container

## Alternatives & Trade-offs

### Port Forwarding

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| A. vsock tunnel | Bypasses VM NAT entirely; works regardless of VM network topology; same pattern as OrbStack/Docker Desktop | Requires userspace proxy on host; adds a connection hop | **Selected** |
| B. crosvm port-forward + nftables DNAT | Kernel-level forwarding, lower latency | Depends on crosvm's NAT config; fragile; requires two layers of NAT rules | Rejected |
| C. Host-side socat/proxy only | Simple to implement | Still needs a path into the container; doesn't solve the routing problem | Rejected |

**Rationale**: vsock tunnel is the industry standard approach. Docker Desktop uses a userspace proxy (`docker-proxy`). OrbStack tunnels through its virtio channel. This decouples port forwarding from the VM's network topology entirely. The host daemon listens on macOS, tunnels bytes over vsock to the agent, which connects to the container's IP:port. Reliable, debuggable, no NAT complexity.

### Cgroups

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| A. cgroups v2 unified hierarchy | Modern, single hierarchy, clean API; default on recent kernels | Requires cgroupv2 mount | **Selected** |
| B. cgroups v1 | Wider legacy support | Deprecated; multiple hierarchies; more complex | Rejected |

**Rationale**: Our custom kernel (6.12.15) supports cgroups v2 natively. Alpine guest already mounts cgroupv2 at `/sys/fs/cgroup`. No reason to use v1.

## Phases

### Phase 1: Cgroups v2 Resource Isolation

**Objective**: Add cgroup-based CPU, memory, and PID limits to containers.

**Expected Results**:
- [ ] New `ResourceLimits` struct with fields: `MemoryMax` (bytes), `CPUMax` (microseconds/period), `PidsMax` (count)
- [ ] `ContainerCreateParams` extended with `Resources ResourceLimits`
- [ ] On `container.start`: cgroup `/sys/fs/cgroup/aetheria/<name>` created, limits written, container PID moved into cgroup
- [ ] On `container.stop`: cgroup cleaned up (rmdir)
- [ ] CLI `create` command accepts `--memory`, `--cpus`, `--pids` flags
- [ ] Builds successfully: `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./cmd/aetheria-agent/` and `go build ./cmd/aetheria/`

**Dependencies**: None

**Risks**: Kernel may not have CONFIG_CGROUP_* options. Verify before implementing.

**Status**: COMPLETE

### Phase 2: Port Forwarding — Agent Side

**Objective**: Implement vsock-based port forwarding in the guest agent. Uses the proven PTY pattern: agent dials host vsock for data channel, daemon bridges to TCP client.

**Protocol (per-connection)**:
1. Host daemon receives TCP connection on forwarded host port
2. Daemon sends RPC `portforward.connect {host_port}` to agent
3. Agent looks up mapping: host_port → container_name + container_port
4. Agent dials container_ip:container_port (TCP inside VM)
5. Agent dials vsock CID=2:1026, sends header `{host_port}\n`
6. Agent bridges vsock ↔ container TCP (bidirectional io.Copy)
7. Daemon matches agent connection (by host_port header) to pending TCP client
8. Daemon bridges TCP client ↔ agent vsock

**Expected Results**:
- [ ] `PortMapping` struct: `HostPort uint16, ContainerPort uint16, Protocol string`
- [ ] `Container` struct extended with `Ports []PortMapping`
- [ ] `ContainerCreateParams` extended with `Ports []PortMapping`
- [ ] New file `cmd/aetheria-agent/portforward.go` with:
  - `handlePortForwardConnect(hostPort)`: looks up mapping, dials container, dials vsock, bridges
  - `handlePortForwardRPC(req)`: routes portforward.connect / portforward.list
- [ ] Port forward info included in `container.list` response
- [ ] Builds successfully: `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./cmd/aetheria-agent/`

**Dependencies**: None (independent of Phase 1)

**Risks**: RPC round-trip adds ~2ms per new TCP connection. Acceptable for HTTP workloads.

**Status**: COMPLETE

### Phase 3: Port Forwarding — Host CLI & Daemon

**Objective**: Implement host-side port forwarding: CLI flags, daemon TCP listener, and vsock bridge.

**Expected Results**:
- [ ] CLI `create` command accepts `-p host:container` flag (e.g., `-p 8080:80`)
- [ ] Port mappings passed through RPC chain: CLI → daemon → agent
- [ ] Host daemon: for each port forward, starts `net.Listen("tcp", "0.0.0.0:{hostPort}")`
- [ ] On incoming TCP: send `portforward.connect` RPC, then accept agent connection on vsock port-1026, bridge
- [ ] New Unix socket listener at `/tmp/aetheria-vsock-3/port-1026` for port forward data channel
- [ ] `aetheria ls` output shows port mappings column
- [ ] Port forward TCP listeners closed on container stop
- [ ] Builds successfully: `go build ./cmd/aetheria/`

**Dependencies**: Phase 2

**Risks**: macOS firewall may prompt for listening ports.

**Status**: COMPLETE

### Phase 4: Integration Verification

**Objective**: Verify both features work end-to-end by building and reviewing code paths.

**Expected Results**:
- [ ] Both binaries compile cleanly (agent linux/arm64, CLI darwin/arm64)
- [ ] Code review: no race conditions in port forwarding goroutines
- [ ] Code review: cgroup paths properly sanitized (no path traversal)
- [ ] Code review: port forward connections properly cleaned up on container stop
- [ ] Container struct serialization includes new fields in `container.list` response

**Dependencies**: Phase 1, 2, 3

**Status**: COMPLETE

## Findings

### F-001: Port forward stop automation
The `rm` command now queries container.list for ports and sends `_daemon.portforward.stop` before stopping.

### F-002: bufio.Reader data loss risk
See [progress/port-forward-cgroup#F-002].

### F-003: Cgroup hierarchy enablement
See [progress/port-forward-cgroup#F-003].

