# Plan: virtio-gpu on macOS (incremental path to gfxstream)

Status: PAUSED

## Phase 2 Assessment (Metal 显示窗口)

**核心障碍**：macOS Cocoa/NSWindow 必须在主线程创建和操作。crosvm 的主线程被
HVF vCPU 运行循环占用。无法从 GPU 工作线程安全地创建和更新窗口。

**可行路径**（需要按顺序实现）：
- 2a: 重构 crosvm 主线程，将 VM 运行移到工作线程，主线程运行 NSApplication
- 2b: 实现 Metal display backend (CAMetalLayer + 帧缓冲 blit)
- 2c: 集成窗口事件处理（关闭、调整大小、鼠标）

**工期估计**：2-3 周（涉及 Objective-C 桥接、Metal 框架、主线程架构重构）
**依赖**：`objc2`、`metal`、`cocoa` crates 或直接 FFI

**建议**：Phase 2 是独立的大型工程项目。建议先完成 aetheria-agent 和容器层
（这些不需要 GPU），再回来做 Metal 显示。
Created: 2026-04-01
Source: Architecture requirement — GPU virtualization for GUI apps and 3D acceleration

## Objective

Implement virtio-gpu device on macOS, incrementally building toward gfxstream+Metal
for production-grade 3D acceleration. Start with software rendering, add display,
then accelerate.

## Alternatives Analysis

| Approach | Effort | Result | Decision |
|----------|--------|--------|----------|
| A: Direct gfxstream port | 3-6 months | Full 3D | Rejected — gfxstream C++ lib has no macOS port |
| B: VirglRenderer + MoltenVK | 4-8 weeks | OpenGL 3D | Rejected — virglrenderer depends on Linux Mesa |
| C: Incremental (2D → display → Vulkan) | Phased | Progressive | **SELECTED** |

**Rationale**: gfxstream requires Vulkan which requires MoltenVK on macOS. The rendering
pipeline has 4 layers (device → rutabaga → display → host GPU), each needing macOS support.
Building bottom-up is the only viable strategy.

## Phase 1: Rutabaga2D device + stub display

**Objective**: Guest sees `/dev/dri/card0`, virtio-gpu device probes successfully.
Software-only rendering via Rutabaga2D backend. No visible display output.

**Expected Results**:
1. `--features gpu` compiles on macOS (with necessary cfg gates and stubs)
2. virtio-gpu device registered on PCI bus (`[1af4:1050]`)
3. Guest kernel prints `virtio_gpu` or `[drm]` driver probe message
4. Guest has `/dev/dri/card0` and `/dev/dri/renderD128`
5. `dmesg | grep drm` shows successful initialization
6. Compiles: `cargo build --no-default-features --features net,gpu --release`

**Dependencies**: None
**Risks**: gpu feature pulls in many Linux-specific dependencies; may need extensive
cfg-gating. rutabaga_gfx external crate may have hidden Linux assumptions.

## Phase 2: macOS Metal display backend

**Objective**: Guest framebuffer renders to a macOS window via Metal/CAMetalLayer.
2D scanout only — no 3D acceleration yet.

**Expected Results**:
1. `gpu_display` crate has a macOS backend (gpu_display_metal.rs or similar)
2. Boot produces a macOS window showing the guest's framebuffer
3. Console text and GUI elements visible
4. Window resizes correctly

**Dependencies**: Phase 1
**Risks**: Metal API complexity, color space conversion, framebuffer format matching.

## Phase 3: MoltenVK integration

**Objective**: Vulkan API available on macOS host via MoltenVK, enabling Vulkan-based
rendering backends in rutabaga_gfx.

**Expected Results**:
1. MoltenVK SDK linked and Vulkan instance createable
2. rutabaga_gfx can initialize with Vulkan support
3. Guest can use Vulkan-capable rendering (virgl or gfxstream in Vulkan mode)
4. Basic 3D demo (glxgears or vkcube) renders in guest

**Dependencies**: Phase 2
**Risks**: MoltenVK license (Apache 2.0 — OK for commercial), Vulkan→Metal
translation gaps, performance overhead.

## Phase 4: gfxstream backend activation

**Objective**: Full gfxstream protocol with Vulkan API forwarding via MoltenVK.
Near-native 3D performance for guest applications.

**Expected Results**:
1. gfxstream native library compiled for macOS (or pre-built)
2. `--gpu backend=gfxstream` works on macOS
3. Guest OpenGL/Vulkan apps render with hardware acceleration
4. Performance within 80% of native for standard benchmarks

**Dependencies**: Phase 3
**Risks**: gfxstream C++ codebase may have hard Linux dependencies beyond Vulkan.
May need to contribute upstream patches.
