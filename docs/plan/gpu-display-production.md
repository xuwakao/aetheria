# Plan: Production GPU Display Pipeline

Created: 2026-04-04T03:00:00Z
Status: ACTIVE
Source: User requirement — "完整可靠的实现" (complete and reliable, not initial version)

## Task Description

Upgrade the SharedMemory display backend from CPU readback fallback to proper GPU resource import path. Implement `import_resource` and `flip_to` on `DisplayShm` so gfxstream's rendered frames flow through the same zero-copy-capable path as Windows/Linux.

## Current Problem

```
CURRENT (CPU readback fallback):
  gfxstream renders → Vulkan texture
    → import_resource_to_display() returns None (SharedMemory doesn't implement it)
    → FALLBACK: transfer_read() copies GPU texture → CPU framebuffer
    → flip() sends to SharedMemory

TARGET (proper import path):
  gfxstream renders → Vulkan texture
    → export_blob() → file descriptor (MoltenVK VkExternalMemoryFd)
    → import_resource() → display backend mmaps the fd
    → flip_to(import_id) → copy from imported mapping to shared memory back buffer
    → signal_frame()
```

## Architecture Decision: Why Not IOSurface Zero-Copy?

IOSurface zero-copy (GPU texture shared directly between processes) would eliminate ALL copies. But it requires:
1. `VK_EXT_metal_objects` in MoltenVK to extract MTLTexture/IOSurface
2. IOSurface ID sent via control socket
3. AetheriaDisplay.app wraps IOSurface as MTLTexture
4. Complex synchronization (fence/semaphore sharing between processes)

**Decision**: Implement `import_resource` with mmap'd fd first. On Apple Silicon (unified memory), the mmap'd fd points to the SAME physical memory as the GPU texture — the memcpy to shared memory back buffer is L2-cache-speed (~50 GB/s). This is effectively zero-copy for practical purposes.

IOSurface path is Phase 2 optimization — eliminates the final memcpy to shared memory.

## Phases

### Phase 1: Implement import_resource on DisplayShm

**Objective**: Add DMA-buf/fd import support to SharedMemory display backend.

**Expected Results**:
- [ ] `ShmSurface` extended with `imported_resources: HashMap<u32, ImportedResource>` storing mmap'd fd pointers
- [ ] `import_resource()` implemented: mmaps the exported fd, stores pointer + size + metadata
- [ ] `release_import()` implemented: munmaps and removes from map
- [ ] `flip_to(import_id)` implemented: copies from imported mmap to shared memory back buffer, signals frame
- [ ] `import_resource_to_display()` in virtio_gpu.rs succeeds on macOS (no longer returns None)
- [ ] Compiles: `RUSTUP_TOOLCHAIN=stable cargo build --release -p gpu_display`

**Dependencies**: None
**Risks**: macOS `export_blob` may return fd type that can't be mmapped. Verify with MoltenVK docs.
**Status**: PENDING

### Phase 2: Enable external_blob for gfxstream on macOS

**Objective**: Configure GpuParameters to enable blob export so gfxstream creates exportable resources.

**Expected Results**:
- [ ] `gpu_params.external_blob = true` set in macOS runner when gfxstream feature active
- [ ] `gpu_params.system_blob = true` set for optimal allocation
- [ ] Compiles: `RUSTUP_TOOLCHAIN=stable cargo build --release -p gpu_display -p devices`

**Dependencies**: Phase 1
**Status**: PENDING

### Phase 3: AetheriaDisplay.app — eliminate unnecessary texture re-upload

**Objective**: When gfxstream writes to shared memory back buffer via flip_to, AetheriaDisplay should use that data directly without redundant operations.

**Expected Results**:
- [ ] MetalRenderer uses `MTLBuffer` backed by shared memory (avoid CPU→GPU texture upload)
- [ ] Or: MTLTexture with shared storage mode on Apple Silicon (unified memory — no copy)
- [ ] Frame rate verified: 60fps at 1080p without dropped frames
- [ ] Compiles: `swift build` in AetheriaDisplay/

**Dependencies**: Phase 1
**Status**: PENDING

### Phase 4: Review and verification

**Expected Results**:
- [ ] Code review: no resource leaks (every import_resource paired with release_import)
- [ ] Code review: mmap error handling (MAP_FAILED check)
- [ ] Code review: thread safety (imports accessed from GPU thread only)
- [ ] Both Rust and Swift compile cleanly
- [ ] gfxstream → import → flip_to → shared memory → Metal render: complete path verified at code level

**Dependencies**: Phase 1-3
**Status**: PENDING

## Findings

