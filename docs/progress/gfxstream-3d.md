# Progress: Gfxstream 3D GPU Acceleration

Created: 2026-04-04T01:00:00Z
Source: [plan/gfxstream-3d]

## Log

### [2026-04-04T01:00:00Z] Planning Complete
**Action**: Created 6-phase plan with virglrenderer-first strategy, gfxstream research in parallel
**Result**: PASS

### Plan Review

| Phase | Dependencies OK | Expected Results Testable | Feasibility | Risks Identified | Stub/Real Marked | Verdict |
|-------|----------------|--------------------------|-------------|-----------------|-----------------|---------|
| 1 | No deps | `file libvirglrenderer.dylib` → Mach-O arm64 | RISK: virglrenderer needs libepoxy + DRM headers. macOS has deprecated OpenGL, no DRM. Must investigate libepoxy macOS support and DRM header stubs. | libepoxy macOS: may need homebrew. DRM: need stub headers. | Real impl | RISK |
| 2 | Phase 1 → dylib + headers | `cargo build --features virgl_renderer -p devices` exit 0 | RISK: rutabaga_gfx is external crate (crates.io). Cannot modify its Cargo.toml. Feature `virgl_renderer` uses pkg-config to find libvirglrenderer. Must set PKG_CONFIG_PATH. | External crate limitation: cannot add #[cfg(target_os)] patches. May need local fork. | Real impl | RISK |
| 3 | Phase 2 → compilable crosvm with virgl | `cargo build --release --features virgl_renderer` exit 0 | Feasible if Phase 2 succeeds — wiring is straightforward config change | Render server: macOS no minijail. Must use in-process (set_use_render_server=false) | Real impl | PASS |
| 4 | Phase 3 → crosvm presents 3D caps | `ls /dev/dri/card0` in guest + `eglinfo` shows Virgl | RISK: guest Mesa needs virgl Gallium driver compiled for aarch64. Alpine's Mesa package may not include virgl driver. | May need custom Mesa build or Ubuntu guest with mesa-utils | Real impl | RISK |
| 5 | Phase 4 → guest has DRM + Mesa | Visual: 3D content in AetheriaDisplay window | Feasible if all prior phases work | Integration complexity: many moving parts | Real impl | PASS |
| 6 | No deps | Written report | Feasible: pure research task | gfxstream may be impractically complex to port | Real research | PASS |

**Dependency graph**: Phase 1 → Phase 2 → Phase 3 → Phase 4 → Phase 5; Phase 6 independent.

**Critical risks identified**:
1. Phase 1 RISK: virglrenderer macOS build — need to verify libepoxy availability and DRM header stubs
2. Phase 2 RISK: rutabaga_gfx external crate — may need local fork to add macOS support
3. Phase 4 RISK: Mesa virgl driver on ARM64 Alpine — may need different guest distro

**Action**: Before proceeding to Phase 1 implementation, need feasibility verification for virglrenderer macOS build. Let me investigate first.

### [2026-04-04T01:10:00Z] Feasibility Research Complete
**Key findings**:
1. virglrenderer v1.1.1 upstream has explicit `with_host_darwin` support in meson.build
2. libepoxy available via Homebrew
3. libdrm NOT required on macOS (optional since upstream supports Darwin)
4. Three independent projects have working macOS builds: UTM, MacPorts, NixOS
5. **Venus + MoltenVK** path recommended (Vulkan forwarding, proven by libkrun/MacPorts)
6. rutabaga_gfx has no OS gate for virgl_renderer — just needs the dylib
7. Meson build: `meson setup build -Dvenus=true -Dvulkan-dload=true -Ddrm-renderers=[] -Dvideo=false`

**Plan update**: Venus+MoltenVK is more practical than VirGL+OpenGL. Aligns with macOS direction (OpenGL deprecated). All Phase 1 RISK items resolved by upstream Darwin support.

### [2026-04-04T01:11:00Z] Starting Phase 1 — Build virglrenderer with Venus for macOS
**Expected results**: virglrenderer v1.1.1 cloned, MoltenVK + libepoxy installed, meson build with Venus, libvirglrenderer.dylib for ARM64.

### [2026-04-04T01:30:00Z] Review: Phase 1

| # | Expected Result | Actual Result | Evidence | Verdict |
|---|-----------------|---------------|----------|---------|
| 1 | virglrenderer v1.1.1 cloned | Cloned to aetheria-virglrenderer/ | `git log --oneline -1` | PASS |
| 2 | MoltenVK installed | `brew install molten-vk` v1.4.1 | Homebrew | PASS |
| 3 | libepoxy installed | `brew install libepoxy` v1.5.10 | Homebrew | PASS |
| 4 | Meson build with Venus | Configured with 3 macOS patches | meson.build, proxy_socket.c, vkr_ring.c | PASS |
| 5 | libvirglrenderer.dylib ARM64 | `file` confirms Mach-O 64-bit arm64, 2.8MB | build/src/libvirglrenderer.1.dylib | PASS |

**Overall Verdict**: PASS
**Findings this phase**: 3

### F-001: virglrenderer v1.1.1 requires 3 macOS patches
1. `meson.build:110` — libdrm required for Venus even on Darwin. Fix: gate with `host_machine.system() != 'darwin'`
2. `meson.build:403` — render server compiled unconditionally with Venus. Fix: skip on Darwin (signalfd/SOCK_CLOEXEC unavailable)
3. `src/proxy/proxy_socket.c:103` — `MSG_CMSG_CLOEXEC` not on macOS. Fix: `#ifdef` guard
4. `src/venus/vkr_ring.c:205` — `clock_nanosleep` not on macOS. Fix: use `nanosleep` on Apple

### F-002: Vulkan found via MoltenVK
Meson detected Vulkan 1.4.341 from MoltenVK. Venus Vulkan forwarding will use MoltenVK → Metal on the host side.

### F-003: render-server option is deprecated in v1.1.1
Venus always enables render server. The `-Drender-server=false` option is ignored. Must patch meson.build to skip server subdir on Darwin.

### [2026-04-04T01:30:30Z] Functional Acceptance: Phase 1
**Build**: `libvirglrenderer.1.dylib` Mach-O arm64 built and installed to `.local/lib/`
**Result**: PASS

## Plan Corrections

## Findings

