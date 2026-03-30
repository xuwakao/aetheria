# ISS-007: SOCK_SEQPACKET replaced with SOCK_STREAM without documentation

Created: 2026-03-31
Status: OPEN
Severity: LOW
Source: Full codebase review (2026-03-31)

## Description

`base/src/sys/macos/net.rs` replaces `SOCK_SEQPACKET` with `SOCK_STREAM` for `UnixSeqpacket::pair()` because macOS does not support `SOCK_SEQPACKET` on Unix domain sockets.

```rust
pub fn pair() -> io::Result<(UnixSeqpacket, UnixSeqpacket)> {
    // macOS does not support SOCK_SEQPACKET, so we use SOCK_STREAM instead.
    let (fd0, fd1) = socketpair(libc::AF_UNIX, libc::SOCK_STREAM, 0)?;
```

## Analysis

### Functional Difference
- `SOCK_SEQPACKET`: Message-oriented, preserves message boundaries, reliable
- `SOCK_STREAM`: Byte-stream, no message boundaries, reliable

### Impact on crosvm
crosvm's Tube uses `UnixSeqpacket` for inter-process communication. Tube messages are serialized with length-prefixed framing, so message boundaries are preserved at the application layer regardless of socket type. The `SOCK_STREAM` substitution is **functionally correct** for this use case.

### Risk
If any code path sends raw bytes without length framing and relies on `SOCK_SEQPACKET` message boundaries, it would break silently. Current audit shows all Tube communication uses `serde` serialization with explicit length headers.

## Recommended Fix

The code change is correct. Add a more detailed comment explaining:
1. Why SOCK_SEQPACKET is unavailable on macOS
2. Why SOCK_STREAM is a safe substitute (Tube uses length-prefixed framing)
3. Reference to the Python test that confirms macOS rejects SOCK_SEQPACKET

## Findings

### F-001: macOS SOCK_SEQPACKET limitation
macOS kernel does not implement SOCK_SEQPACKET for AF_UNIX sockets. `socketpair(AF_UNIX, SOCK_SEQPACKET, 0)` returns `EPROTONOSUPPORT` (errno 43). This is a well-known macOS limitation.
