# Plan: Interactive Shell (PTY)

Created: 2026-04-02
Status: ACTIVE
Source: P0 — core UX for `aetheria shell <name>`

## Task Description

Implement interactive terminal access to containers. `aetheria shell ubuntu`
should give a full bash/sh session with proper terminal handling (arrow keys,
tab completion, Ctrl+C, window resize).

Current state: `container.exec` is request-response (send cmd, get output).
No streaming, no PTY, no stdin forwarding.

## Alternatives & Trade-offs

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| PTY over vsock JSON-RPC | Reuses existing protocol | JSON encoding of binary stream is slow, can't handle raw terminal escape sequences well | Rejected |
| Dedicated vsock stream for PTY | Clean separation, raw bytes, low latency | Need new vsock port/connection, more complex | **Selected** |
| SSH server inside container | Standard tool, rich features | Heavy dependency, key management, overkill | Rejected |

## Architecture

```
macOS Terminal (raw mode)
    ↕ stdin/stdout bytes
aetheria CLI
    ↕ Unix socket → daemon
daemon
    ↕ dedicated vsock stream (port 1025)
agent
    ↕ PTY master fd
nsenter → /bin/sh (container)
    ↕ PTY slave fd
```

Key: a second vsock connection (port 1025) carries raw PTY bytes.
The JSON-RPC channel (port 1024) sends control messages (start shell,
window resize, exit notification).

## Phases

### Phase 1: Agent PTY exec with nsenter

**Objective**: Agent creates a PTY, runs nsenter+shell attached to it,
and streams PTY I/O over a new vsock connection.

**Expected Results**:
- [ ] New RPC: `container.shell` returns a session ID
- [ ] Agent opens PTY via `pty.Open()` (Go creack/pty or os/exec with PTY)
- [ ] Agent runs `nsenter -t PID -p -m -u -i [-n] -- /bin/sh -l` with PTY
- [ ] Agent accepts a second vsock connection on port 1025 for PTY stream
- [ ] Raw bytes flow: vsock ↔ PTY master fd
- [ ] `go build` succeeds

**Dependencies**: None

**Status**: PENDING

### Phase 2: Host CLI terminal forwarding

**Objective**: CLI sets terminal to raw mode, forwards stdin/stdout over
the daemon socket to the PTY vsock stream.

**Expected Results**:
- [ ] CLI `shell` command sets terminal to raw mode (no echo, no line buffering)
- [ ] CLI sends stdin bytes to daemon → daemon forwards to agent vsock 1025
- [ ] Agent PTY output bytes flow back: agent → daemon → CLI → stdout
- [ ] Ctrl+C, arrow keys, tab completion work correctly
- [ ] Shell exit restores terminal to normal mode
- [ ] Window resize (SIGWINCH) forwarded to PTY

**Dependencies**: Phase 1

**Status**: PENDING

### Phase 3: Integration test

**Objective**: End-to-end `aetheria shell alpine` works interactively.

**Expected Results**:
- [ ] `aetheria shell alpine` opens interactive sh session
- [ ] `ls`, `cat`, `echo` work
- [ ] Tab completion works
- [ ] Ctrl+C sends SIGINT to container process
- [ ] `exit` returns to macOS terminal cleanly

**Dependencies**: Phase 1, 2

**Status**: PENDING

## Findings
