# Progress: virtio-devices

Created: 2026-03-31T18:00:00+08:00
Source: [plan/virtio-devices]

## Log

### [2026-03-31T18:00] META-PHASE A — Planning
**Action**: Created 6-phase plan for production-grade virtio device stack.
Supersedes [plan/virtio-stack] which had incorrect assumptions:
- p9 was assumed to be "just ungating" — actual audit shows it requires Linux namespaces, external crate with likely Linux syscalls, and missing macOS config infrastructure.
- Replaced Phase 2 (9p) with virtio-fs (FUSE-based, better long-term investment).
- Added proper VirtioPciDevice wrapping with tube pairs (Linux pattern analysis).

Key findings from codebase audit:
- [F-001] virtio-blk macOS backend complete, needs PCI wrapping only
- [F-002] Linux uses 5 tube pairs per device for control communication
- [F-003] p9 NOT portable without major effort (REJECTED)
- [F-005] macOS run_config passes empty device list — root cause of missing virtio devices

### [2026-03-31T18:10] META-PHASE B — Plan Review

| # | Item | Verdict | Evidence |
|---|------|---------|----------|
| 1 | Dependency validation | PASS | Phase 1 independent; Phase 2 depends on Phase 1 (/dev/vda); Phase 3 independent; Phase 4 depends on 1-2 (rootfs) + 3 (vmnet); Phase 5 depends on 1-2; Phase 6 depends on 1-5. No circular deps. |
| 2 | Expected results precision — Phase 1 | PASS | Compile check + runtime `virtio_blk virtio0` detection + `fdisk -l /dev/vda`. Verified by dmesg grep + fdisk output. |
| 3 | Expected results precision — Phase 2 | PASS | Alpine login prompt + `cat /etc/alpine-release` + `apk --version` + `poweroff` clean exit. All verifiable via serial console. |
| 4 | Expected results precision — Phase 3 | PASS | Compile + unit test for VmnetTap creation. Runtime packet I/O verified in Phase 4. |
| 5 | Expected results precision — Phase 4 | PASS | DHCP lease + ping 8.8.8.8 + DNS resolution + apk install. All measurable. |
| 6 | Expected results precision — Phase 5 | RISK | FUSE passthrough server audit scope unknown. May discover platform-specific syscalls that require significant workaround. **Mitigation**: conduct audit as first step of Phase 5; if blocking, pivot to alternative approach. |
| 7 | Feasibility — Phase 1 | PASS | BlockAsync macOS backend complete [F-001]. VirtioPciDevice wrapping is well-documented pattern from Linux codebase [F-002]. Main work is wiring, not new logic. |
| 8 | Feasibility — Phase 2 | PASS | Alpine minirootfs is 3MB download + mkfs.ext4. switch_root is standard busybox tool. CONFIG_EXT4_FS=y confirmed built-in. |
| 9 | Feasibility — Phase 3 | RISK | vmnet.framework API is C/Objective-C with GCD callbacks. Bridging to Rust requires careful unsafe code. QEMU has working implementation as reference. Rust `vmnet` crate exists but assumes macOS 15+. **Mitigation**: write our own FFI bindings for broader compatibility. |
| 10 | Feasibility — Phase 4 | PASS | crosvm's virtio-net device is cross-platform (only TapT backend is platform-specific). With VmnetTap from Phase 3, the device should "just work". |
| 11 | Feasibility — Phase 5 | RISK | virtio-fs FUSE server uses `fuse` crate which may have Linux assumptions. Audit required. [F-004] |
| 12 | Stub vs real | PASS | All phases produce real, functional implementations. No stubs planned. Phase 3 unit test verifies real vmnet interface creation. |
| 13 | Alternatives completeness | PASS | p9 rejection backed by concrete codebase evidence [F-003]. vmnet vs utun vs socket_vmnet properly evaluated in deprecated plan. virtio-fs selection justified by performance advantage over 9p. |

**Actions**: No plan changes. Phase 5 and Phase 3 carry known risks with documented mitigations.

### [2026-03-31T18:15] Starting Phase 1 — virtio-blk device registration
**Expected results**:
- run_config reads cfg.disks, creates BlockAsync with PCI wrapping
- Each disk gets MSI-X, ioevent, vm_control tubes
- Devices passed to build_vm (non-empty devs vector)
- Compiles + kernel detects virtio_blk virtio0 + /dev/vda exists

## Plan Corrections

## Findings
