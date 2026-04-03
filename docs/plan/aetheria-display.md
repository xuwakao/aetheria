# Plan: AetheriaDisplay.app — GPU Display Rendering

Created: 2026-04-04T00:00:00Z
Status: COMPLETED
Source: User request — "AetheriaDisplay.app详细设计和实现"

## Task Description

Implement the AetheriaDisplay.app — a standalone macOS application (Swift + Metal) that renders the VM's framebuffer to a native window. The crosvm VMM writes frames to shared memory; the display app reads and renders them via Metal at 60fps.

This is a two-component system:
1. **crosvm display backend** (Rust) — implements `GpuDisplaySurface` trait, writes XRGB8888 frames to shared memory, signals frame ready via Unix socket
2. **AetheriaDisplay.app** (Swift) — reads shared memory, creates MTLTexture, renders via MetalKit, handles input events

## Architecture

```
crosvm (Rust, background process)
┌───────────────────────────────────┐
│ virtio-gpu device                 │
│   ↓ RESOURCE_FLUSH               │
│ GpuDisplaySurface::flip()         │
│   ↓ copy to shared memory        │
│ SharedMemoryDisplayBackend        │
│   ↓ write 1 byte to socket       │
│ Unix socket (frame ready signal)  │──────┐
│   ↓ mmap'd file                   │      │
│ /tmp/aetheria-display.shm  ──────┼──────┼──→ shared memory
└───────────────────────────────────┘      │
                                           │
AetheriaDisplay.app (Swift, foreground)    │
┌───────────────────────────────────┐      │
│ NSApplication + NSWindow          │      │
│   ↕                               │      │
│ DispatchSource.read(socket) ◄────┼──────┘
│   ↓ frame ready notification      │
│ Read shared memory header         │
│   ↓                               │
│ MTLTexture.replace(region, data)  │
│   ↓                               │
│ MTKView.draw() → Metal render     │
│   ↓                               │
│ CAMetalLayer.present()            │
│                                   │
│ Input: NSEvent → Unix socket      │
│   → crosvm virtio-input device    │
└───────────────────────────────────┘
```

### Shared Memory Layout

```
Offset  Size     Field
0       4        magic (0x41455448 "AETH")
4       4        version (1)
8       4        width
12      4        height
16      4        stride (bytes per row)
20      4        format (DRM_FORMAT_XRGB8888 = 0x34325258)
24      4        frame_seq (incremented on each flip, for dirty detection)
28      4        flags (bit 0: cursor visible)
32      —        padding to 4096 (page-aligned)
4096    w*h*4    framebuffer data (XRGB8888)
```

### Control Socket Protocol

Unix socket at `/tmp/aetheria-display.sock`:
- crosvm → app: `F` (1 byte, frame ready)
- crosvm → app: `R` + width(u32le) + height(u32le) (resize)
- app → crosvm: `K` + scancode(u32le) + pressed(u8) (key event)
- app → crosvm: `M` + x(u32le) + y(u32le) + buttons(u8) (mouse move)
- app → crosvm: `C` + x(u32le) + y(u32le) + button(u8) + pressed(u8) (mouse click)

## Alternatives & Trade-offs

### IPC Mechanism

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| A. mmap'd file + Unix socket | Simple, portable, zero-copy | Requires explicit signaling | **Selected** |
| B. IOSurface | macOS-native zero-copy, GPU-optimized | Requires IOSurface C API from Rust (complex FFI) | Future Phase 2 |
| C. XPC service | Apple-recommended IPC | Heavyweight for frame data, requires Info.plist | Rejected |
| D. Mach ports | Low-level, fast | Complex API, fragile | Rejected |

**Rationale**: mmap'd file is the simplest working approach. IOSurface can be added later as an optimization (avoids CPU→GPU copy). The control socket handles signaling with minimal overhead.

### Rendering API

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| A. MetalKit (MTKView) | Built-in vsync, simple API | Slightly less control | **Selected** |
| B. Raw Metal + CAMetalLayer | Maximum control | More boilerplate | Future optimization |
| C. Core Graphics (CGImage) | Simple 2D | No GPU acceleration, CPU-bound | Rejected |

## Phases

### Phase 1: Shared Memory Display Backend (crosvm Rust)

**Objective**: Implement a new `GpuDisplaySurface` in crosvm that writes framebuffer data to a mmap'd shared memory file and signals via Unix socket.

**Expected Results**:
- [ ] New file `gpu_display/src/gpu_display_shm.rs` implementing `GpuDisplaySurface` trait
- [ ] `framebuffer()` returns a `GpuDisplayFramebuffer` pointing into the shared memory region
- [ ] `flip()` increments `frame_seq` in shared memory header and writes `F` byte to control socket
- [ ] Shared memory file created at `/tmp/aetheria-display.shm` with header + framebuffer data
- [ ] Control socket listener at `/tmp/aetheria-display.sock`
- [ ] `DisplayBackend::SharedMemory` variant added to enum in `devices/src/virtio/gpu/mod.rs`
- [ ] macOS platform code selects `SharedMemory` backend by default
- [ ] Compiles: `cargo build --release` in aetheria-crosvm

**Dependencies**: None
**Risks**: Rust FFI for mmap on macOS. Verified: `libc::mmap` works on macOS (POSIX-compliant).
**Status**: PENDING

### Phase 2: AetheriaDisplay.app Skeleton (Swift)

**Objective**: Create a minimal Swift macOS app that connects to shared memory and control socket, reads the framebuffer header, and displays a blank window.

**Expected Results**:
- [ ] New Xcode project or Swift Package at `AetheriaDisplay/`
- [ ] `main.swift` with NSApplication setup
- [ ] `DisplayWindow.swift` with NSWindow (resizable, titled "Aetheria")
- [ ] `ShmReader.swift` — opens `/tmp/aetheria-display.shm`, mmaps it, reads header (width/height/format)
- [ ] `ControlSocket.swift` — connects to `/tmp/aetheria-display.sock`, reads frame notifications
- [ ] Window dimensions match shared memory header (width × height)
- [ ] Compiles: `swiftc` or `swift build`

**Dependencies**: Phase 1 (needs shared memory format to read)
**Status**: PENDING

### Phase 3: Metal Rendering Pipeline (Swift)

**Objective**: Render the shared memory framebuffer to the window via Metal at 60fps.

**Expected Results**:
- [ ] `MetalRenderer.swift` — MTKViewDelegate implementation
- [ ] Creates MTLTexture (BGRA8Unorm, matching XRGB8888 layout)
- [ ] On frame notification: `texture.replace(region:, data:)` from shared memory
- [ ] Renders textured quad to screen via simple vertex/fragment shaders
- [ ] `Shaders.metal` — passthrough vertex shader + texture sampler fragment shader
- [ ] Achieves 60fps vsync rendering (MTKView.preferredFramesPerSecond = 60)
- [ ] Window content updates when crosvm flips a new frame
- [ ] Compiles and links with Metal framework

**Dependencies**: Phase 2
**Risks**: XRGB8888 vs BGRA8Unorm byte order. On little-endian: XRGB8888 in memory is [B, G, R, X] which maps to BGRA8Unorm with alpha=X. Verified: this is the standard conversion.
**Status**: PENDING

### Phase 4: Input Handling (Swift → crosvm)

**Objective**: Capture keyboard and mouse events from the window and send them to crosvm via the control socket.

**Expected Results**:
- [ ] `InputHandler.swift` — captures NSEvent keyboard and mouse events
- [ ] Key events: scancode + pressed/released, sent as `K` message via control socket
- [ ] Mouse move: x,y coordinates relative to window, sent as `M` message
- [ ] Mouse click: button + pressed/released, sent as `C` message
- [ ] crosvm reads input messages from control socket and injects into virtio-input device
- [ ] Guest receives keyboard and mouse input

**Dependencies**: Phase 3 (window must exist to capture events)
**Risks**: macOS scancode → Linux scancode mapping. May need translation table.
**Status**: PENDING

### Phase 5: Integration with aetheria CLI

**Objective**: Wire AetheriaDisplay.app launch into the `aetheria run` flow.

**Expected Results**:
- [ ] `run.sh` launches AetheriaDisplay.app after crosvm starts (or crosvm launches it)
- [ ] crosvm passes `--gpu backend=shared_memory` flag
- [ ] Display app auto-discovers shared memory and socket paths
- [ ] Graceful shutdown: display app exits when crosvm exits
- [ ] `aetheria run` shows display window automatically

**Dependencies**: Phase 1-4
**Status**: PENDING

## Findings

