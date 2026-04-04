# Plan: Gfxstream 3D GPU Acceleration on macOS

Created: 2026-04-04T01:00:00Z
Status: ACTIVE
Source: User request — "推进 gfxstream 3D 加速"

## Task Description

Enable 3D GPU acceleration for the Aetheria VM on macOS by porting Google's gfxstream library. The guest runs OpenGL ES / Vulkan apps; gfxstream translates these commands to host Vulkan; MoltenVK translates Vulkan to Metal.

Pipeline: `Guest OpenGL/Vulkan → virtio-gpu → Rutabaga → gfxstream → Vulkan → MoltenVK → Metal`

## Architecture

```
Guest VM (Linux)
  App (OpenGL ES / Vulkan)
    ↓ virtio-gpu command stream
crosvm GPU device
  Rutabaga (Rust crate)
    ↓ FFI
  libgfxstream_backend.dylib (C++, Google)
    ↓ Vulkan API calls
  MoltenVK (libMoltenVK.dylib)
    ↓ Metal API calls
  Apple GPU (M-series)
    ↓ rendered frames
  SharedMemory display backend
    ↓ mmap'd framebuffer
  AetheriaDisplay.app (Swift/Metal)
```

## Complexity Assessment

This is a **multi-week, multi-component porting effort**:

| Component | Source | Lines | Build System | macOS Status |
|-----------|--------|-------|-------------|-------------|
| gfxstream | github.com/google/gfxstream | ~200K C++ | Meson | **NOT PORTED** |
| MoltenVK | github.com/KhronosGroup/MoltenVK | ~100K | Xcode/CMake | Available (binary) |
| minigbm | chromium.googlesource.com | ~10K C | Make | **NOT PORTED** |
| rutabaga_gfx | crates.io | ~5K Rust | Cargo | Needs feature gating |

## Alternatives & Trade-offs

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| A. gfxstream + MoltenVK (full) | Production-quality 3D, same as ChromeOS ARCVM | Massive porting effort (gfxstream C++ has deep Linux dependencies) | **Target** |
| B. virglrenderer + OpenGL (intermediate) | Simpler than gfxstream, Mesa-based | Slower (virgl overhead), OpenGL only (no Vulkan) | **Phase 1 target** |
| C. Software rendering (SwiftShader) | Works without GPU, no driver dependencies | ~100x slower, unusable for real 3D apps | Rejected |
| D. virtio-gpu venus (Vulkan passthrough) | Direct Vulkan, best performance | Requires kernel 5.16+ venus driver, complex setup | Future |

**Strategy**: Start with virglrenderer (Approach B) as a stepping stone — it's simpler to build on macOS and provides immediate 3D capability. Then tackle gfxstream (Approach A) for full Vulkan.

## Phases

### Phase 1: Build virglrenderer with Venus for macOS (ARM64)

**Objective**: Compile virglrenderer v1.1.1 as a macOS dylib with Venus (Vulkan forwarding) backend. Venus forwards guest Vulkan calls to host MoltenVK → Metal.

**Expected Results**:
- [ ] virglrenderer v1.1.1 source cloned (upstream has Darwin support)
- [ ] MoltenVK SDK installed (via Homebrew or Vulkan SDK)
- [ ] libepoxy installed via Homebrew
- [ ] Meson build: `meson setup build -Dvenus=true -Dvulkan-dload=true -Ddrm-renderers=[] -Dvideo=false -Drender-server=false`
- [ ] `libvirglrenderer.dylib` built for ARM64
- [ ] Build verified: `file libvirglrenderer.dylib` shows Mach-O arm64

**Dependencies**: None
**Risks**: Upstream Darwin support may still have edge cases. MacPorts and NixOS have working builds as reference.
**Status**: PENDING

### Phase 2: Build rutabaga_gfx with virgl_renderer on macOS

**Objective**: Enable the `virgl_renderer` feature of rutabaga_gfx crate, linking against the macOS virglrenderer dylib.

**Expected Results**:
- [ ] rutabaga_gfx Cargo.toml updated with macOS-specific virglrenderer pkg-config path
- [ ] `cargo build --features virgl_renderer` succeeds for the gpu_display and devices crates
- [ ] RutabagaComponentType::VirglRenderer selectable on macOS

**Dependencies**: Phase 1 (libvirglrenderer.dylib)
**Risks**: rutabaga_gfx may have Linux-specific FFI (DRM ioctls, dma-buf). Need to stub or #[cfg] gate.
**Status**: PENDING

### Phase 3: Wire virgl into crosvm macOS runner

**Objective**: Enable `GpuMode::ModeVirglRenderer` on macOS and configure crosvm to use it.

**Expected Results**:
- [ ] macOS runner passes `DisplayBackend::SharedMemory` + VirglRenderer component to GPU device
- [ ] crosvm `--gpu mode=virglrenderer` accepted on macOS
- [ ] GPU device initializes with Virgl capsets
- [ ] `cargo build --release --features virgl_renderer` succeeds for full crosvm binary

**Dependencies**: Phase 2
**Risks**: Render server process model — macOS has no minijail. Must use in-process rendering.
**Status**: PENDING

### Phase 4: Guest kernel DRM + Mesa driver

**Objective**: Ensure the guest kernel has virtio-gpu DRM driver and Mesa virgl Gallium driver.

**Expected Results**:
- [ ] Kernel config: CONFIG_DRM_VIRTIO_GPU=y verified in aetheria_arm64_defconfig
- [ ] Guest rootfs includes Mesa virgl driver (libGL.so, libEGL.so)
- [ ] `ls /dev/dri/card0 /dev/dri/renderD128` works in guest
- [ ] `glxinfo` or `eglinfo` in guest shows Virgl renderer

**Dependencies**: Phase 3 (crosvm must present virtio-gpu with 3D caps)
**Risks**: Mesa virgl driver may need specific Gallium driver compilation for ARM64.
**Status**: PENDING

### Phase 5: Integration test — 3D rendering in guest

**Objective**: Run a 3D application in the guest and see it rendered in AetheriaDisplay.app.

**Expected Results**:
- [ ] `glxgears` or equivalent runs in guest with 3D acceleration
- [ ] Framebuffer shows rendered 3D content (not software fallback)
- [ ] Performance: >30fps for simple 3D scene
- [ ] No crashes or GPU hangs during 60-second test

**Dependencies**: Phase 4
**Status**: PENDING

### Phase 6: Research gfxstream macOS build feasibility

**Objective**: Determine exact requirements for building gfxstream C++ library on macOS. This is RESEARCH, not implementation.

**Expected Results**:
- [ ] gfxstream source cloned and analyzed
- [ ] Complete dependency tree mapped (ANGLE, swiftshader, protobuf, etc.)
- [ ] Meson build files analyzed for Linux-specific assumptions
- [ ] List of all macOS-incompatible APIs identified (DRM, EGL, etc.)
- [ ] Estimated LOC to patch documented
- [ ] Written feasibility report in docs/plan/

**Dependencies**: None (can run in parallel with Phase 1-5)
**Risks**: gfxstream may have deep ANGLE/swiftshader dependencies that make macOS port impractical.
**Status**: PENDING

## Findings

