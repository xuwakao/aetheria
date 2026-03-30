# Progress: issue-cleanup

Created: 2026-03-31T14:00:00+08:00
Source: [plan/issue-cleanup]

## Log

### [2026-03-31T14:00] META-PHASE A — Planning
**Action**: Created 7-phase plan to resolve all 16 issues from codebase review.

### [2026-03-31T14:05] META-PHASE B — Plan Review

| # | Item | Verdict | Evidence |
|---|------|---------|----------|
| 1 | Dependency validation | PASS | Phase 1→2→4 chain; Phase 3,5,7 independent; Phase 6 depends on Phase 1 |
| 2 | Expected results precision | PASS | Each phase has compile + boot test verification |
| 3 | Feasibility | PASS | All changes are code cleanup / refactoring, no new features except Phase 6 (stdin) |
| 4 | Risk identification | RISK | Phase 4 (PSCI) may break boot if kernel expects specific PSCI behavior. Mitigation: test each PSCI call. |
| 5 | Stub vs real | PASS | Phase 5 explicitly documents stubs. Phase 6 is real implementation. |
| 6 | Alternatives | PASS | N/A — fixes are straightforward |

### [2026-03-31T14:05] Starting Phase 1 — Debug logging cleanup
**Expected results**: Remove all debug MMIO/exit/IRQ logging, FDT dump, duplicate earlycon. Build + boot passes.

### [2026-03-31T16:00] Starting Phase 4 — PSCI proper implementation (ISS-010)
**Expected results**:
- PSCI handling removed from vcpu_loop match block
- Hypercall bus routes PSCI calls to a new PsciDevice
- PSCI_SYSTEM_OFF/RESET return ExitState change
- PSCI_CPU_ON returns PSCI_NOT_SUPPORTED (honest)
- Boot test: PSCI still probed successfully by kernel

### Review: Phase 4

| # | Expected Result | Actual Result | Evidence | Verdict |
|---|-----------------|---------------|----------|---------|
| 1 | PSCI handling removed from vcpu_loop match block | Inline PSCI match removed; vcpu_loop delegates to `hypercall_bus.handle_hypercall(abi)` | `src/crosvm/sys/macos.rs` vcpu_loop Hypercall arm | PASS |
| 2 | Hypercall bus routes PSCI calls to new PSCI device | PsciDevice registered on hypercall_bus for 32-bit (0x84000000..0x84000010) and 64-bit (0xC4000000..0xC4000010) ranges | `devices/src/psci.rs`, `src/crosvm/sys/macos.rs` registration block | PASS |
| 3 | PSCI_SYSTEM_OFF/RESET trigger ExitState change | PsciDevice sets `exit_request` atomic; vcpu_loop checks after hypercall and returns ExitState::Stop or ExitState::Reset | `devices/src/psci.rs:124-131`, `src/crosvm/sys/macos.rs` vcpu_loop | PASS [UNVERIFIED at runtime — SYSTEM_OFF/RESET not triggered during boot test] |
| 4 | PSCI_CPU_ON returns PSCI_NOT_SUPPORTED (honest) | Returns -1 (PSCI_NOT_SUPPORTED) instead of 0 (SUCCESS) | `devices/src/psci.rs:113-116` | PASS |
| 5 | Boot test: PSCI still probed successfully by kernel | `psci: PSCIv1.0 detected in firmware`, `psci: Trusted OS migration not required` | Boot test output | PASS |

**Overall Verdict**: PASS
**Notes**: SYSTEM_OFF/RESET exit path tested at code level (correct atomic flag + vcpu_loop check) but not triggered at runtime during boot test since the initramfs shell doesn't invoke shutdown.

### [2026-03-31T16:30] Phase 4 Functional Acceptance
- Build: `cargo build --no-default-features` compiles (0 errors, 17 warnings — all pre-existing)
- Boot: kernel boots, PSCI 1.0 detected, interactive shell reached
- PASS

### [2026-03-31T16:35] Starting Phase 7 — Document remaining architecture issues (ISS-004, ISS-011)
**Expected results**: ISS-004 and ISS-011 documented with detailed analysis, DEFERRED status, solution paths.

### Review: Phase 7

| # | Expected Result | Actual Result | Evidence | Verdict |
|---|-----------------|---------------|----------|---------|
| 1 | ISS-004 updated with detailed analysis + marked DEFERRED | Already completed in earlier phase: root cause, 4 solution options, findings F-001/F-002 | `docs/issue/ISS-004-single-vcpu-main-thread.md` | PASS |
| 2 | ISS-011 updated with dependency on ISS-004 + marked DEFERRED | Already completed: references ISS-004 as root cause, solution path documented | `docs/issue/ISS-011-no-vm-control.md` | PASS |
| 3 | Both reference recommended solution (Option A from ISS-004) | ISS-004: "Option A is the cleanest long-term solution"; ISS-011: "Implement after ISS-004 is resolved" | Both issue files | PASS |

**Overall Verdict**: PASS
**Notes**: Phase 7 was effectively completed during Phase 5 (document limitations). No additional changes needed.

### [2026-03-31T16:35] Phase 7 Functional Acceptance
- No code changes required (documentation only phase)
- PASS

### [2026-03-31T16:40] META-PHASE D — Completion
All 7 phases complete. Final issue status:

| Issue | Status | Resolution |
|-------|--------|------------|
| ISS-001 | RESOLVED | Debug logging removed |
| ISS-002 | RESOLVED | Hardcoded FDT dump removed |
| ISS-003 | RESOLVED | Hardcoded kernel params removed |
| ISS-004 | DEFERRED | Architecture limitation documented with solution path |
| ISS-005 | DEFERRED | macOS 14 GIC fallback — intentional limitation |
| ISS-006 | RESOLVED | Named constants added |
| ISS-007 | RESOLVED | SOCK_SEQPACKET documented |
| ISS-008 | DEFERRED | Debug stubs — implemented when needed |
| ISS-009 | RESOLVED | Interactive console implemented |
| ISS-010 | RESOLVED | PSCI moved to proper bus device |
| ISS-011 | DEFERRED | Depends on ISS-004 |
| ISS-012 | RESOLVED | GIC config memory leak fixed |
| ISS-013 | RESOLVED | Duplicate constants eliminated |
| ISS-014 | RESOLVED | Duplicate earlycon removed |
| ISS-015 | DEFERRED | Simplified guest memory — intentional limitation |
| ISS-016 | DEFERRED | Feature-gated stubs — intentional limitation |

10 resolved, 6 deferred (all with documented rationale and solution paths).

## Plan Corrections

## Findings
