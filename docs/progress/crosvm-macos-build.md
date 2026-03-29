# Progress: crosvm-macos-build

Created: 2026-03-30T04:45:00+08:00
Source: [plan/crosvm-macos-build]

## Log

### [2026-03-30T04:45] META-PHASE A - Planning
**Action**: Assessed state per RULE 7. Highest priority: unblock crosvm binary build (blocks Phase 5 boot test). Strategy: iterative fix of unconditional dependency crates via `cargo build --no-default-features`. Initial failures: metrics, net_util, disk.
**Result**: PASS
**Cross-ref**: [plan/crosvm-macos-build]

### [2026-03-30T04:48] META-PHASE B - Plan Review Checklist

**1. Dependency validation**: Phase 2 depends on Phase 1 (need binary to boot). Linear, no circular deps. PASS.

**2. Expected results precision**: Phase 1 uses `cargo build --no-default-features` as the concrete test — binary output. Phase 2 uses serial console output as evidence. Both verifiable. PASS.

**3. Feasibility assessment**: Phase 1 is feasible — each fix is a macOS sys module (we've done this 5+ times already). Phase 2 **RISK** — runtime HVF testing may surface new bugs not caught by compilation. Acceptable risk.

**4. Risk identification**: Main risk is unknown depth (how many crates?). Initial scan shows 3 immediate failures. The `src/crosvm/sys.rs` and `src/sys.rs` (crosvm main binary entry) will be the hardest — they contain the full VM setup logic. RISK flagged.

**5. Stub vs real**: Plan explicitly states "real where POSIX allows, stub where not" and requires documentation for each. PASS.

**6. Alternatives**: Three evaluated with rationale. PASS.

**Review result**: Plan is sound. Risks acknowledged. Proceed.

## Plan Corrections

## Findings

### [2026-03-30T05:10] Phase 1 - Partial progress, PAUSED
**Action**: Fixed 3 crates: metrics (copied Linux WaitContext-based controller), net_util (TapT stub trait), disk (simplified disk ops). Prebuilts was already fixed. Build still failing — disk crate has deeper issues (FlockOperation not on macOS, is_block_device_file conditional). Each crate has multiple sub-issues beyond simple sys module addition.
**Result**: IN PROGRESS — 3 fixed, but the crosvm binary build chain is deep. Need a more systematic approach or this will take dozens of iterations.
**Cross-ref**: [plan/crosvm-macos-build#Phase1]

### F-001: crosvm macOS porting is larger than iterative fixes
The crosvm workspace has deep Linux assumptions throughout. The `cfg_if` platform dispatch is only the surface — individual functions within modules also use Linux-specific APIs (flock, O_DIRECT, is_block_file, etc.) behind non-gated code. A full macOS port requires either:
1. Adding macOS cfg gates to hundreds of individual functions across ~30 crates
2. Or building a minimal crosvm binary that skips most of the device/disk/net layers
Option 2 is more practical for initial boot testing.
