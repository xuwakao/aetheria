# Plan: kernel-config

Created: 2026-03-29T20:30:00+08:00
Status: COMPLETED
Source: Architecture decision (memory/architecture_decision.md), prior research conversation on OrbStack kernel analysis

## Task Description

Create Aetheria's Linux kernel defconfig — a shared kernel configuration based on mainline Linux LTS that supports running multiple Linux distribution containers (Ubuntu, Fedora, Alpine, Arch, etc.) and future Android containers within a single crosvm-managed VM. The kernel must support all necessary virtio devices for crosvm, full namespace/cgroup isolation, LSM stacking (AppArmor + SELinux), dynamic memory management, and Android binder/binderfs.

Key insight from OrbStack kernel analysis [plan/kernel-config#F-001]: OrbStack's kernel is NOT a heavily customized minimal kernel. It is close to a standard ARM64 defconfig (~1462 lines, 851 =y, 595 =m, 368 physical hardware driver configs). Its performance advantage comes from host-side implementation, not kernel magic. The only notable kernel-level optimizations are KPTI disable (Apple Silicon) and virtio-balloon + KSM for dynamic memory.

Strategy: start from the OrbStack public defconfig (`orbstack/linux-macvirt` `mac-pub` branch `arch/arm64/configs/defconfig`), add what's missing for Aetheria (virtio-gpu, vsock, binder, LSM, VirtioFS), and strip unnecessary physical hardware drivers.

## Alternatives & Trade-offs

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| A: Start from OrbStack defconfig, add/modify | Proven config for shared-kernel VM; known working on Apple VZ; minimal risk | Some configs specific to Apple VZ may not apply to crosvm; public version may not be latest | **Selected** |
| B: Start from upstream ARM64 defconfig | Clean baseline; no VZ-specific assumptions | Need to manually enable all container/VM features; more work; risk of missing something | Rejected — more work for no clear benefit |
| C: Start from Kata Containers kernel config | Optimized for VM boot speed | Designed for micro-VM (single process), missing almost all container/distro features | Rejected — wrong use case |
| D: Start from WSL2 kernel config | Proven shared-kernel config | x86_64 only; Hyper-V specific; would need heavy adaptation for ARM64 + crosvm | Rejected — wrong platform |

## Phases

### Phase 1: Acquire and Analyze OrbStack Defconfig

**Objective**: Download the OrbStack public defconfig, document its complete structure, and identify all gaps relative to Aetheria's requirements.

**Expected Results**:
- [x] OrbStack defconfig saved to `aetheria-kernel/reference/orbstack-defconfig`
- [ ] Gap analysis document listing all missing configs for Aetheria (virtio-gpu, vsock, VirtioFS, binder, LSM, etc.)
- [ ] List of unnecessary physical hardware drivers to remove

**Dependencies**: None

**Status**: COMPLETE

### Phase 2: Create Aetheria ARM64 Defconfig

**Objective**: Produce `aetheria-kernel/configs/aetheria_arm64_defconfig` that adds all required Aetheria features to the OrbStack base while removing unnecessary hardware drivers.

**Expected Results**:
- [ ] `aetheria_arm64_defconfig` exists with all required configs:
  - virtio: blk, net, fs (VirtioFS), vsock, gpu, console, balloon, PCI, MMIO
  - container: full namespace set, cgroup v1+v2, overlayfs, seccomp
  - Android: binder, binderfs, fuse, PSI
  - LSM: AppArmor + SELinux stacking
  - network: bridge, veth, full netfilter/iptables/nftables, conntrack, TUN, macvlan
  - memory: KSM, balloon, transparent hugepages
  - performance: io_uring, BPF, no KPTI (ARM64)
  - binfmt_misc for Rosetta/QEMU user-mode
  - Docker-in-container support (user_ns, overlay, cgroup delegation)
- [ ] Unnecessary physical hardware drivers (USB, WiFi, Bluetooth, sound, physical GPU drivers) removed or set to =n
- [ ] Config is well-commented with section headers

**Dependencies**: Phase 1

**Status**: COMPLETE

### Phase 3: Create x86_64 Defconfig Variant

**Objective**: Produce `aetheria-kernel/configs/aetheria_x86_64_defconfig` adapted for x86_64/KVM target (Linux and Windows hosts).

**Expected Results**:
- [ ] `aetheria_x86_64_defconfig` exists with equivalent feature set
- [ ] x86_64-specific adjustments: KPTI can be disabled via boot param (not build-time on x86), appropriate x86 virtio configs, no ARM-specific configs
- [ ] Documented differences from ARM64 variant

**Dependencies**: Phase 2

**Status**: COMPLETE

### Phase 4: Kernel Build Verification (ARM64)

**Objective**: Compile the ARM64 kernel from mainline LTS source using the defconfig and verify it produces a valid vmlinux.

**Expected Results**:
- [ ] `aetheria-kernel/scripts/build-kernel.sh` exists and works
- [ ] Kernel compiles without errors using latest LTS source
- [ ] Output `vmlinux` file exists and is valid (file type check)
- [ ] Kernel size documented (target: ~20-30MB)
- [ ] Build instructions documented in `aetheria-kernel/README.md`

**Dependencies**: Phase 2

**Status**: COMPLETE

### Phase 5: Commit and Update Documentation

**Objective**: Commit all kernel config work, update architecture documentation with accurate kernel details.

**Expected Results**:
- [ ] All files committed to aetheria-kernel submodule
- [ ] `docs/architecture.md` kernel section updated with accurate config list
- [ ] `docs/research/architecture-comparison.md` findings updated

**Dependencies**: Phase 3, Phase 4

**Status**: COMPLETE

## Findings

### F-001: OrbStack Kernel Is Not Minimal

OrbStack's public defconfig (`orbstack/linux-macvirt`, `mac-pub` branch) contains 1462 config lines (851 =y, 595 =m), including 368 physical hardware driver configs. It is close to a standard ARM64 distribution kernel, not a minimal VM kernel. Performance advantages come from host-side implementation (native Swift app, custom file sharing, dynamic balloon control), not kernel-level optimizations. The only kernel-level optimizations are KPTI disable and virtio-balloon + KSM.

Source: Direct analysis of `https://raw.githubusercontent.com/orbstack/linux-macvirt/mac-pub/arch/arm64/configs/defconfig`

### F-002: OrbStack Uses 9P Not VirtioFS

OrbStack's defconfig includes `CONFIG_9P_FS=y` and `CONFIG_NET_9P_VIRTIO=y` but does NOT include `CONFIG_VIRTIO_FS`. This contradicts OrbStack's documentation which mentions "VirtioFS". Possible explanation: VZ framework handles VirtioFS host-side transparently, or the public defconfig is not the latest version.

Aetheria uses crosvm which requires explicit `CONFIG_VIRTIO_FS=y` in the kernel for VirtioFS support.

Source: grep of OrbStack defconfig; OrbStack architecture docs

### F-003: OrbStack Has No vsock, GPU, binder, or LSM

OrbStack's public defconfig is missing: `VIRTIO_VSOCKETS`, `VIRTIO_GPU`/`DRM_VIRTIO_GPU`, `ANDROID_BINDER_IPC`/`ANDROID_BINDERFS`, `SECURITY_APPARMOR`/`SECURITY_SELINUX`, `IO_URING`. These are all required by Aetheria's architecture and must be added.

Source: grep of OrbStack defconfig
