# ISS-017: Tube recv returns 0 on macOS SOCK_STREAM — no message framing

Created: 2026-03-31
Status: IN-PROGRESS
Severity: CRITICAL (blocks all virtio device I/O)
Source: [plan/virtio-devices#Phase2]

## Description

`Tube::recv()` calls `next_packet_size()` which uses `recvfrom(fd, NULL, 0, MSG_TRUNC|MSG_PEEK, ...)` to determine the message size before allocating the receive buffer. This relies on `SOCK_SEQPACKET` semantics where `MSG_TRUNC` returns the full datagram size.

On macOS, `UnixSeqpacket::pair()` creates `SOCK_STREAM` sockets (macOS does not support AF_UNIX SOCK_SEQPACKET). `MSG_TRUNC` on SOCK_STREAM does NOT report the message size — it discards excess data. With a 0-byte buffer, `recvfrom` returns 0 immediately, causing `recv_with_max_fds` to allocate a 0-byte buffer, read 0 bytes, and return `Error::Disconnected`.

## Root Cause

`base/src/sys/unix/net.rs:350-377`: `next_packet_size()` assumes `SOCK_SEQPACKET` semantics.

On SOCK_SEQPACKET:
- `recvfrom(fd, NULL, 0, MSG_TRUNC|MSG_PEEK)` → returns full message size (datagram size)

On SOCK_STREAM (macOS):
- `recvfrom(fd, NULL, 0, MSG_TRUNC|MSG_PEEK)` → returns 0 (no data to report in stream mode)

## Impact

All Tube-based IPC on macOS fails silently:
- VirtioPciDevice ioevent registration (immediate recv of 0 → Disconnected)
- Any future Tube-based control communication

Currently masked by the fact that most Tube paths haven't been exercised until virtio-blk activation.

## Fix

Implement length-prefix framing for SOCK_STREAM on macOS:
- `send_with_fds`: prepend a 4-byte LE length header before the JSON payload
- `recv_with_fds` / `next_packet_size`: read the 4-byte header first, then read exactly that many bytes

This is the standard approach for message-oriented protocols over byte streams (used by gRPC, HTTP/2, WebSocket, etc.).

## Findings
