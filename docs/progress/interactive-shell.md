# Progress: Interactive Shell (PTY)

Created: 2026-04-02
Source: [plan/interactive-shell]

## Log

### [2026-04-02T05:00] META-PHASE A — Planning
Designed PTY-over-vsock architecture. Key decision: use a dedicated vsock
stream (port 1025) for raw PTY bytes, separate from JSON-RPC control channel.

### Plan Review

| # | Item | Verdict | Evidence |
|---|------|---------|----------|
| 1 | Dependency graph | PASS | Phase 1→2→3 linear |
| 2 | Phase 1 precision | PASS | Measurable: PTY created, nsenter runs, bytes flow over vsock |
| 3 | Phase 1 feasibility | PASS | Go stdlib has os/exec with PTY support via SysProcAttr.Setsid+Setctty; or use creack/pty (vendored). nsenter already works. |
| 4 | Phase 2 precision | PASS | Measurable: raw mode, Ctrl+C works, arrow keys work |
| 5 | Phase 2 feasibility | PASS | Go x/term for raw mode, or direct tcgetattr/tcsetattr via syscall |
| 6 | Phase 3 precision | PASS | Manual interactive test |
| 7 | PTY in Go without deps | RISK | Go stdlib doesn't have pty.Open(). Need either creack/pty dep or manual posix_openpt/grantpt/unlockpt via syscall. Manual approach is ~30 lines. |

Risk mitigation: implement PTY open via syscall (no external dep).

## Plan Corrections

## Findings
