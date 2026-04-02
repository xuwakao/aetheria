# Plan: virtio-vsock on macOS

Status: COMPLETED
Created: 2026-04-01
Source: Architecture requirement — host↔guest communication for aetheria-agent

## Objective

Implement virtio-vsock device for macOS using userspace backend (no vhost kernel module). Enable bidirectional host↔guest communication via AF_VSOCK sockets, which is the foundation for aetheria-agent control plane.

## Alternatives Analysis

| Approach | Pros | Cons | Decision |
|----------|------|------|----------|
| A: Port Windows userspace impl | Proven in crosvm, full protocol support | ~1400 LOC, complex async I/O | **SELECTED** |
| B: vhost-user backend | Clean separation, code reuse | Extra process, complex setup | Rejected — overhead for single-VM |
| C: Minimal Unix socket bridge | Simple, fast to implement | Incomplete vsock semantics | Rejected — won't support AF_VSOCK |

## Phase 1: VirtioDevice skeleton + protocol types

**Objective**: Vsock device compiles, registers with PCI bus, guest driver probes it.

**Expected Results**:
1. `devices/src/virtio/vsock/sys/macos.rs` contains VsockConfig + Vsock structs
2. Vsock implements VirtioDevice trait (features, config, activate)
3. Device registered in `src/crosvm/sys/macos.rs` with `--cid` parameter
4. Guest kernel prints `virtio_transport` or `vsock` probe message on boot
5. Compiles: `cargo build --no-default-features --features net --release`

**Dependencies**: None

## Phase 2: Worker thread + queue handling

**Objective**: Worker thread processes TX/RX/event virtqueues. Packets are read from TX queue and dispatched by opcode.

**Expected Results**:
1. Worker spawned on activate(), processes queues in async event loop
2. TX queue: reads virtio_vsock_hdr, dispatches by op (REQUEST/RW/SHUTDOWN/CREDIT)
3. RX queue: can write response packets (RST, RESPONSE, CREDIT_UPDATE) back to guest
4. Event queue: can send TRANSPORT_RESET
5. Guest `socket(AF_VSOCK, ...)` returns fd (not ENOSYS)

**Dependencies**: Phase 1

## Phase 3: Connection management + Unix socket backend

**Objective**: Full bidirectional data transfer. Guest connects to host port → data flows both ways.

**Expected Results**:
1. Guest-initiated connections: OP_REQUEST → Unix socket connect → OP_RESPONSE
2. Host-initiated connections: Unix socket listen → OP_REQUEST to guest
3. Data transfer: OP_RW packets ↔ Unix socket read/write
4. Credit-based flow control (buf_alloc, fwd_cnt)
5. Connection teardown: OP_SHUTDOWN/RST properly cleanup
6. Unix socket path: `/tmp/aetheria-vsock-{cid}/port-{N}`

**Dependencies**: Phase 2

## Phase 4: Integration test

**Objective**: End-to-end host↔guest vsock communication verified.

**Expected Results**:
1. Guest can connect to host vsock port, send/receive data
2. Host can connect to guest vsock port, send/receive data
3. Multiple concurrent connections work
4. `socat` or `ncat --vsock` in guest can talk to host listener
5. No resource leaks on connection close

**Dependencies**: Phase 3

## Risks

| Risk | Mitigation |
|------|-----------|
| cros_async executor differences on macOS | Use simple poll-based executor if needed |
| Guest AF_VSOCK driver needs kernel config | CONFIG_VIRTIO_VSOCKETS=y already in defconfig |
| Credit flow control edge cases | Port exact logic from Windows, test with large transfers |
