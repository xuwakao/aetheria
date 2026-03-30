# Progress: virtio-stack

Created: 2026-03-31T17:00:00+08:00
Source: [plan/virtio-stack]

## Log

### [2026-03-31T17:00] META-PHASE A — Planning
**Action**: Created 5-phase plan for virtio device stack on macOS.
- Phase 1: virtio-blk + Alpine rootfs
- Phase 2: virtio-9p file sharing
- Phase 3: virtio-net + vmnet.framework
- Phase 4: virtio-vsock
- Phase 5: Integration test

Key research findings:
- Virtio-PCI transport already works on macOS (no changes needed)
- virtio-blk macOS backend exists, just not wired into run_config
- virtio-9p is platform-agnostic, Linux-gated only by cfg_if
- vmnet.framework provides NAT networking (requires root)
- vmnet has single-iovec and GCD-thread-callback constraints

### [2026-03-31T17:10] META-PHASE B — Plan Review

| # | Item | Verdict | Evidence |
|---|------|---------|----------|
| 1 | Dependency validation | PASS | Phase 1 independent; Phase 2 depends on Phase 1 (virtio-PCI proven); Phase 3 depends on Phase 1 (rootfs for testing); Phase 4 depends on Phase 1; Phase 5 depends on 1-4. No circular dependencies. |
| 2 | Expected results precision | PASS | Each phase has compile check + runtime verification (device detection, mount, ping, etc.) |
| 3 | Feasibility — Phase 1 | PASS | virtio-blk backend exists (`block/sys/macos.rs`), just needs wiring. PCI path confirmed working in [F-001]. Main work: run_config integration + rootfs script. |
| 4 | Feasibility — Phase 2 | RISK | p9 crate platform compatibility unverified [F-003]. If p9 uses Linux-specific syscalls (openat2, renameat2), macOS shims needed. **Mitigation**: audit p9 crate before starting Phase 2. |
| 5 | Feasibility — Phase 3 | RISK | vmnet.framework has two known constraints [F-005, F-006]: single-iovec writes and GCD callback threading. Both have proven solutions (QEMU's implementation). Root requirement is acceptable for initial dev. |
| 6 | Feasibility — Phase 4 | RISK | vsock protocol state machine is non-trivial. Credit-based flow control can be deferred. Linux vhost-vsock implementation is kernel-assisted; userspace version needs careful design. |
| 7 | Stub vs real | PASS | Phase 1-3 are real implementations. Phase 4 explicitly notes minimal subset first. Phase 5 is integration. |
| 8 | Alternatives completeness | PASS | Networking: 4 options evaluated with clear rationale. File sharing: 4 options. Rootfs: 3 options. Vsock: 2 options. |
| 9 | Risk identification | PASS | Each phase lists risks with mitigations. Phase 3 has the most risk (new framework integration + threading). |

**Actions taken**:
- No plan changes needed. Risks are acknowledged with mitigations.
- F-003 marked [UNVERIFIED] — will verify before Phase 2 execution.

## Plan Corrections

## Findings
