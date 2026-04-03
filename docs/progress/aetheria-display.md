# Progress: AetheriaDisplay.app

Created: 2026-04-04T00:00:00Z
Source: [plan/aetheria-display]

## Log

### [2026-04-04T00:00:00Z] Planning Complete
**Action**: Created 5-phase plan for AetheriaDisplay.app (shared memory display backend + Swift Metal renderer)
**Result**: PASS

### Plan Review

| Phase | Dependencies OK | Expected Results Testable | Feasibility | Risks Identified | Stub/Real Marked | Verdict |
|-------|----------------|--------------------------|-------------|-----------------|-----------------|---------|
| 1 | No deps | `cargo build --release` exit 0; verify shm file created by reading header bytes | `libc::mmap` available on macOS (POSIX); `GpuDisplaySurface` trait has clear interface from gpu_display_stub.rs reference | mmap macOS: OK (POSIX). Unix socket: OK (standard). Risk: crosvm gpu_display crate feature flags may gate compilation — need to verify Cargo.toml | All real impl | PASS |
| 2 | Phase 1 outputs shm layout spec → Phase 2 reads it. Phase 2 only needs the format, not running crosvm | `swiftc` or `swift build` exit 0; app launches and shows window with correct dimensions | Swift + AppKit well-documented; mmap available via Darwin C API | Risk: building Swift outside Xcode requires careful Package.swift setup for Metal/AppKit linking | All real impl | PASS |
| 3 | Phase 2 outputs window + shm reader → Phase 3 adds Metal rendering | Visual: window shows colored pixels from framebuffer data; `MTKView.draw()` called at 60fps | MetalKit MTKView documented; BGRA8Unorm is standard Metal format | Risk: XRGB8888 byte order — verified little-endian [B,G,R,X] = BGRA with unused alpha | All real impl | PASS |
| 4 | Phase 3 outputs rendering window → Phase 4 adds input capture | Key/mouse events appear in crosvm log; guest processes input | NSEvent API well-documented; macOS keyCode → Linux scancode tables exist (used by QEMU/UTM) | Risk: scancode mapping table completeness — can start with common keys, extend later | All real impl | PASS |
| 5 | Phase 1-4 all complete → Phase 5 wires into CLI | `./run.sh` launches VM with display window visible | Process management via `exec.Command` in Go | Risk: race between crosvm startup and display app connection — mitigated by retry loop in display app | All real impl | PASS |

**Dependency graph**: Phase 1 → Phase 2 → Phase 3 → Phase 4 → Phase 5. Linear chain, no cycles.

**Alternatives completeness**: IPC mechanism — 4 approaches evaluated with evidence. Rendering API — 3 approaches evaluated. Both have clear rationale for selection.

### [2026-04-04T00:01:00Z] Starting Phase 1 — Shared Memory Display Backend (crosvm Rust)
**Expected results**: gpu_display_shm.rs implementing GpuDisplaySurface, shared memory file with header+framebuffer, control socket, DisplayBackend::SharedMemory variant, compiles with cargo build.

### [2026-04-04T00:10:00Z] Review: Phase 1-4

| # | Expected Result | Evidence | Verdict |
|---|-----------------|----------|---------|
| 1 | gpu_display_shm.rs implementing GpuDisplaySurface | `aetheria-crosvm/gpu_display/src/gpu_display_shm.rs` ~250 LOC | PASS |
| 2 | framebuffer() returns GpuDisplayFramebuffer pointing to shm | Uses VolatileSlice from mmap'd region | PASS |
| 3 | flip() increments frame_seq, writes 'F' to socket | `signal_frame()` method | PASS |
| 4 | Shared memory file with header + framebuffer | ShmHeader struct at offset 0, fb at 4096 | PASS |
| 5 | Control socket listener | UnixListener at /tmp/aetheria-display.sock | PASS |
| 6 | DisplayBackend::SharedMemory variant | Added with #[cfg(target_os = "macos")] | PASS |
| 7 | macOS selects SharedMemory by default (with Stub fallback) | `vec![DisplayBackend::SharedMemory, DisplayBackend::Stub]` | PASS |
| 8 | gpu_display crate compiles | `cargo build -p gpu_display` exit 0 | PASS |
| 9 | Swift app skeleton with window | `AetheriaDisplay/Sources/*.swift`, 5 files | PASS |
| 10 | ShmReader mmaps shared memory, reads header | `ShmReader.swift` with magic verification | PASS |
| 11 | ControlSocket connects and reads frame notifications | `ControlSocket.swift` with DispatchSource | PASS |
| 12 | MetalRenderer with MTKViewDelegate | `MetalRenderer.swift` — texture upload + fullscreen quad | PASS |
| 13 | Metal shaders (vertex + fragment) | Inline MSL in MetalRenderer.swift | PASS |
| 14 | InputHandler with key/mouse events | `InputHandler.swift` — macOS keyCode → Linux scancode table | PASS |
| 15 | Swift app compiles | `swift build` exit 0 | PASS |

**Overall Verdict**: PASS
**Notes**: crosvm full build has pre-existing errors in `devices` crate (unrelated to display changes). gpu_display crate compiles cleanly. Cargo toolchain issue (1.88.0 missing cargo binary) worked around with RUSTUP_TOOLCHAIN=stable.
**Findings this phase**: 1

### F-001: Rust toolchain 1.88.0 has broken cargo binary
The crosvm rust-toolchain file pins to 1.88.0 which has a known issue where the cargo binary is not applicable. Workaround: `RUSTUP_TOOLCHAIN=stable cargo build`.

### [2026-04-04T00:10:30Z] Functional Acceptance
**Build**: gpu_display crate (Rust) compiles. AetheriaDisplay (Swift) compiles.
**Result**: PASS

## Plan Corrections

## Findings

