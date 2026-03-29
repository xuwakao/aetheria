# Plan: crosvm-macos-build

Created: 2026-03-30T04:45:00+08:00
Status: PAUSED
Source: [progress/crosvm-hvf-fixes#Phase3] — 30 crates need macOS modules, [plan/crosvm-hvf#Phase5] — boot test blocked

## Task Description

Build a minimal crosvm binary on macOS ARM64 that can boot a Linux kernel with serial console output. This requires adding macOS platform modules to all unconditional dependency crates that currently fail compilation.

Strategy: iterative — `cargo build --no-default-features` → fix the first failing crate → rebuild → repeat. Only fix crates that are in the compilation path for `--no-default-features` (minimal binary). Feature-gated crates (GPU, audio, USB, etc.) are out of scope.

## Alternatives & Trade-offs

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| A: Fix crates iteratively until minimal binary compiles | Targeted, only fixes what's needed | May be many iterations | **Selected** |
| B: Fix all 30 crates upfront | Complete | Wastes effort on feature-gated crates we don't need yet | Rejected |
| C: Create a separate minimal crosvm binary that bypasses most crates | Fastest to boot | Diverges from upstream, hard to maintain | Rejected |

## Phases

### Phase 1: Fix Unconditional Dependency Crates

**Objective**: Add macOS platform modules to all crates that fail `cargo build --no-default-features -p crosvm`. Each fix is a macOS sys module — either a real implementation (if the Linux version is POSIX-portable) or a stub that returns `Err(Unsupported)` (for Linux-only features like epoll fd_executor).

**Expected Results** (concrete, verifiable):
- [ ] `cargo build --no-default-features` progresses past metrics, net_util, disk (currently failing)
- [ ] Each fix: real implementation where POSIX allows, documented stub where not
- [ ] All new macOS sys modules documented in progress with rationale (real vs stub)
- [ ] `cargo build --no-default-features` either succeeds or reaches a genuinely new category of blocker

**Risks**:
- Unknown depth — we don't know how many crates are in the `--no-default-features` path until we iterate
- Some crates may have deep Linux dependencies that require significant porting (e.g., devices with ioctl, arch with KVM-specific setup)
- macOS main binary entry point (src/crosvm/sys.rs, src/sys.rs) likely needs substantial work

**Dependencies**: None

**Status**: PENDING

### Phase 2: Boot Verification

**Objective**: Boot the Aetheria ARM64 kernel using the compiled crosvm binary on macOS, getting serial console output.

**Expected Results**:
- [ ] `crosvm run --no-default-features` (or equivalent) with `--kernel vmlinux-arm64` starts
- [ ] Serial console output visible (kernel boot messages)
- [ ] Kernel reaches init or panics with "no init found" (proves VM works)
- [ ] Code-signed with `entitlements.plist` for HVF access

**Risks**:
- HVF backend code has never been tested at runtime — may crash on first `hv_vm_create` call
- ARM64 vCPU initialization may need additional register setup not yet implemented
- crosvm's arch layer (aarch64 setup, FDT, PSCI) may have macOS-specific issues

**Dependencies**: Phase 1

**Status**: PENDING

## Findings
