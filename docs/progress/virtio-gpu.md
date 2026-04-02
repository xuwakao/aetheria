# Progress: virtio-gpu

Created: 2026-04-01
Source: [plan/virtio-gpu]

## Log

### [2026-04-01T08:30] META-PHASE A — Planning
Investigated gfxstream + rutabaga_gfx macOS feasibility.
Key finding: gfxstream C++ library has no macOS port; Vulkan not native on macOS.
Selected incremental approach: 2D → Metal display → MoltenVK → gfxstream.

### [2026-04-01T08:30] META-PHASE B — Plan Review

| # | Item | Verdict | Evidence |
|---|------|---------|----------|
| 1 | Dependency validation | PASS | Phase 1→none; 2→1; 3→2; 4→3. Linear chain. |
| 2 | Expected results precision | PASS | Each phase has concrete verification (PCI ID, /dev/dri, window, glxgears). |
| 3 | Feasibility — Phase 1 | RISK | gpu feature has extensive Linux deps; may need heavy cfg-gating. rutabaga_gfx external crate assumptions unknown. |
| 4 | Feasibility — Phase 2 | RISK | Metal display backend is new code. No existing reference for crosvm + Metal. |
| 5 | Feasibility — Phase 3 | RISK | MoltenVK integration with rutabaga_gfx untested. May need upstream patches. |
| 6 | Feasibility — Phase 4 | HIGH RISK | gfxstream C++ lib may have Linux-only assumptions beyond Vulkan. |
| 7 | Stub vs real | PASS | Phase 1 uses real Rutabaga2D. Phase 2+ are real implementations. |

**Action**: Proceed with Phase 1. Phase 3-4 risks are accepted as future work.

### [2026-04-01T09:40] Phase 1 — Rutabaga2D + stub display

Resolved compilation errors across 5 crates:
- vm_control: MacosDisplayMode/MacosMouseMode, GpuRenderServerParameters
- gpu_display: stub MacosDisplayT, AsRawDescriptor, GpuDisplayExt
- vhost_user_backend/gpu: stub Options + start_platform_workers
- crosvm config: macOS GpuRenderServerParameters import
- crosvm macos: GPU device creation with shared memory client

### Review: Phase 1

| # | Expected Result | Actual Result | Evidence | Verdict |
|---|-----------------|---------------|----------|---------|
| 1 | `--features gpu` compiles on macOS | Compiles clean | `Finished release` | PASS |
| 2 | PCI device `[1af4:1050]` | `pci 0000:00:05.0: [1af4:1050] type 00 class 0x038000` | Boot log | PASS |
| 3 | Guest DRM driver probes | `[drm] Initialized virtio_gpu 0.1.0` | Boot log | PASS |
| 4 | Features reported | `+edid +resource_blob +host_visible` | Boot log | PASS |

**Overall Verdict**: PASS
**Findings this phase**: 2

### F-001: GPU device requires shared_memory_vm_memory_client
VirtioPciDevice::new asserts shared_memory_vm_memory_client.is_some() when
the device has shared memory regions. GPU has shmem → must pass Some(client).

### F-002: CONFIG_DRM=m incompatible with module-less rootfs
DRM and DRM_VIRTIO_GPU were =m (modules) but rootfs has no /lib/modules.
Changed to =y (built-in) to fix.

## Plan Corrections

## Findings
