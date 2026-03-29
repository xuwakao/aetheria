# 架构对比研究报告

## 1. 研究背景

Aetheria 的目标是构建一个跨平台（macOS / Windows / Linux）的轻量级 Linux 容器运行环境。在确定最终架构之前，我们对所有现有方案进行了全面的调研和对比。

## 2. 现有项目总览

| 项目 | 类型 | 平台 | 开源 | 定位 |
|------|------|------|------|------|
| OrbStack | 单 VM + 共享内核 | macOS | 否 | Docker 替代 + Linux machine |
| WSL 2 | 单 VM + 共享内核 | Windows | 内核开源 | Windows 上的 Linux 环境 |
| Apple Containerization | micro-VM per container | macOS | 是 | Docker 替代（macOS 26+） |
| Lima | 每 machine 一个 VM | macOS/Linux | 是 | 轻量 Linux VM |
| Colima | Lima 封装 | macOS/Linux | 是 | 简化的 Docker 环境 |
| Podman Machine | 每 machine 一个 VM | 跨平台 | 是 | rootless 容器 |
| UTM | 传统完整 VM | macOS | 是 | 通用虚拟机管理器 |
| ARCVM | 单 VM + Android | Chrome OS | 部分 | Chrome OS 上的 Android |
| WSA | 单 VM + Android | Windows | 否 | Windows 上的 Android（已停止） |
| Waydroid | LXC 容器（非 VM） | Linux | 是 | Linux 上的 Android |
| ATL | API 翻译层 | Linux | 是 | 类 Wine 方式跑 Android（极早期） |

## 3. 三种核心架构模型

### 3.1 模型 A：单 VM + 共享内核 + 容器隔离

**代表项目：OrbStack, WSL 2**

```
Host
└── 单个 Linux VM
    └── 一个共享内核
        ├── nspawn/LXC 容器 1: Ubuntu
        ├── nspawn/LXC 容器 2: Fedora
        └── nspawn/LXC 容器 3: Arch
```

**工作原理：**
- 使用平台原生虚拟化（VZ / Hyper-V）启动一个 Linux VM
- VM 内运行一个共享的 Linux 内核
- 每个 "machine" 实际上是 VM 内部的系统容器（systemd-nspawn 或 LXC）
- 容器拥有独立的 rootfs、init 系统、用户空间，但共享内核

**OrbStack 实现细节：**
- 虚拟化：Apple Virtualization.framework
- 语言：Swift + Go + Rust + C，所有核心服务从零自研
- 内核：自己维护的定制 Linux 内核（[orbstack/linux-macvirt](https://github.com/orbstack/linux-macvirt)），紧跟 mainline 最新版（截至 2026 年 3 月为 6.17.8）
- 文件共享：VirtioFS + 自定义动态缓存优化
- 网络：自研虚拟网络栈，NAT，自定义 DNS 转发，域名 `*.orb.local`，高达 45Gbps 吞吐
- 隔离：systemd-nspawn 或 LXD（OrbStack 闭源，具体机制未公开），支持 15 个 Linux 发行版
- 图形：**不支持**（纯 CLI 环境，无 GPU 加速）
- x86 模拟：Rosetta

**WSL 2 实现细节：**
- 虚拟化：Hyper-V（Type-1 hypervisor）
- 内核：微软维护的定制内核（[microsoft/WSL2-Linux-Kernel](https://github.com/microsoft/wsl2-linux-kernel)），基于 LTS 6.6.y
- 文件共享：9P 协议（性能较差，跨 VM 边界 I/O 是已知瓶颈）
- 网络：NAT 或 Mirrored mode
- 启动：< 1 秒
- 系统调用兼容性：100%（真实 Linux 内核）

**优势：**
- 多 machine 的边际成本几乎为零（只是一个新 nspawn 容器）
- 新建 machine 瞬间完成
- 完整的 Linux 发行版体验（systemd、包管理器、SSH）
- 内存 / CPU 占用最优

**劣势：**
- 实现复杂度极高（需要自研 agent、容器管理、网络栈）
- machine 间隔离较弱（共享内核，内核漏洞影响所有 machine）
- 内核版本由宿主固定，用户不可选
- 如果要自己维护定制内核，工作量巨大

### 3.2 模型 B：每个 machine 一个独立 VM

**代表项目：Lima, Colima, Podman Machine, UTM**

```
Host
├── VM 1: Ubuntu（独立内核）
├── VM 2: Fedora（独立内核）
└── VM 3: Arch（独立内核）
```

**工作原理：**
- 每个 machine 启动一个独立的虚拟机
- 每个 VM 有自己的内核和完整的 OS

**Lima 实现细节：**
- 虚拟化后端：QEMU 或 Apple VZ（macOS 13.5+）
- 语言：Go（74.7%）+ Shell
- 内核：不自己维护，使用发行版 cloud image 自带的内核
- 文件共享：reverse-sshfs 或 VirtioFS（VZ 模式）
- 通信：Guest agent（Go），负责端口转发和文件同步
- 跨架构：QEMU user mode 或 Rosetta（VZ 模式）

**优势：**
- 实现简单——调虚拟化 API 启动 VM 即可
- 每个 VM 完全独立，硬件级隔离
- 不需要自己维护内核
- 用户可以选择任意发行版和内核版本

**劣势：**
- 每个 VM 都有完整的资源开销（内核 + 系统服务）
- 不能高效运行大量 machine
- 启动较慢

### 3.3 模型 C：每个容器一个 micro-VM

**代表项目：Apple Containerization, Kata Containers**

```
Host
├── micro-VM 1: 精简内核 + vminitd + nginx
├── micro-VM 2: 精简内核 + vminitd + postgres
└── micro-VM 3: 精简内核 + vminitd + redis
```

**工作原理：**
- 每个容器运行在自己的轻量级虚拟机中
- VM 内只有精简内核 + 极简 init + 容器进程，没有完整 userland
- 硬件级隔离，但启动极快（亚秒级）

**Apple Containerization 实现细节：**
- 虚拟化：Apple Virtualization.framework
- 语言：纯 Swift
- Init：vminitd（Swift 编译的静态二进制，musl libc）
- 通信：vsock + gRPC（vminitd 在 vsock port 1024 上暴露 gRPC API）
- 文件系统：EXT4 block device 直接挂载（Swift 原生 ext4 实现）
- 网络：每个容器独立 IP（vmnet 框架，macOS 26+）
- 内核：使用 Kata Containers 项目的预编译内核（最小化配置）
- 启动：亚秒级（最小化内核 + 最小 rootfs）
- 支持每容器指定不同内核版本

**micro-VM 里有什么：**

micro-VM 内部的进程可以看到一个真实的、完整的 Linux 内核接口（/proc, /sys, 所有系统调用，无 seccomp 过滤）。内核只是在配置上精简（去掉不需要的驱动），但对进程暴露的 API 是完整的。安全不靠限制进程权限，靠 VM 硬件边界。

| 对比 | 传统 VM | micro-VM |
|------|---------|----------|
| 内核大小 | ~30-50MB | ~5-10MB |
| userland | 完整发行版 | 只有 vminitd 一个二进制 |
| PID 数量 | 几十个 | 2 个（init + 你的进程） |
| 启动时间 | 几秒到几十秒 | < 1 秒 |
| 内存占用 | 200MB+ | ~20-30MB |
| 能否 SSH | 能 | 不能（没有 sshd、没有 shell） |

**优势：**
- 最强隔离 + 最快启动
- 每容器独立内核，互不影响
- EXT4 block device 直通，I/O 性能最好
- 开源（Apple 官方）

**劣势：**
- 不是 "Linux machine"——没有交互式发行版体验
- 不能 SSH 进去，不能装包
- 需要 macOS 26+

## 4. 关键维度对比

### 4.1 隔离强度

| 架构 | machine 之间 | machine 与 host |
|------|-------------|----------------|
| A（共享内核） | Linux namespace（弱，内核漏洞可逃逸） | 硬件虚拟化（强） |
| B（独立 VM） | 硬件虚拟化（强） | 硬件虚拟化（强） |
| C（micro-VM） | 硬件虚拟化（强） | 硬件虚拟化（强） |

### 4.2 资源开销

| 场景 | A（共享内核） | B（独立 VM） | C（micro-VM） |
|------|-------------|-------------|--------------|
| 第 1 个 machine | ~100MB | ~100MB | ~30MB |
| 第 N 个 machine | **几乎为零** | 又 100MB+ | 又 30MB |
| 10 个 machine | ~150MB | ~1GB+ | ~300MB |

### 4.3 启动速度

| 场景 | A（共享内核） | B（独立 VM） | C（micro-VM） |
|------|-------------|-------------|--------------|
| 冷启动（第一个） | 几秒 | 几秒~几十秒 | < 1 秒 |
| 新建 machine | **瞬间** | 几秒~几十秒 | < 1 秒 |

### 4.4 用户体验

| 维度 | A（共享内核） | B（独立 VM） | C（micro-VM） |
|------|-------------|-------------|--------------|
| 感觉像什么 | 完整 Linux 发行版 | 完整 Linux 发行版 | 一个隔离的进程 |
| 有 systemd | ✅ | ✅ | ❌ |
| 能装包 | ✅ | ✅ | ❌ |
| 能 SSH | ✅ | ✅ | 不适用 |
| 内核版本可选 | 否（宿主固定） | 是（发行版自带） | 是（每容器可不同） |

### 4.5 文件 I/O 性能

| 场景 | A（共享内核） | B（独立 VM） | C（micro-VM） |
|------|-------------|-------------|--------------|
| guest 内部 | ext4 on virtio-blk，快 | 同左 | ext4 block device 直通，最快 |
| host↔guest 共享 | VirtioFS（有优化空间） / 9P（慢） | VirtioFS / reverse-sshfs | 不适用 |

## 5. 内核策略对比

| 项目 | 自己维护内核？ | 内核来源 | 版本策略 |
|------|-------------|---------|---------|
| OrbStack | ✅ 深度定制 | linux-macvirt fork | 紧跟 mainline 最新 |
| WSL 2 | ✅ 中度定制 | microsoft/WSL2-Linux-Kernel | LTS 保守（6.6.y） |
| Apple Container | ❌ | Kata Containers 预编译 | LTS |
| Lima | ❌ | 发行版 cloud image 自带 | 取决于发行版 |
| ARCVM | ✅ | Android Common Kernel | ACK 发布节奏 |

**OrbStack 内核特点：**
- 仓库 [orbstack/linux-macvirt](https://github.com/orbstack/linux-macvirt) 公开但不提供最新版
- 针对 Apple Virtualization.framework (macvirt) 做了专门的补丁和优化
- 用户不可替换——OrbStack 的优化与内核深度绑定
- 几乎每次 OrbStack 更新都包含内核更新

**WSL 2 内核特点：**
- 仓库 [microsoft/WSL2-Linux-Kernel](https://github.com/microsoft/wsl2-linux-kernel) 完全开源
- 基于 LTS 分支，专用配置 `Microsoft/config-wsl`
- Hyper-V guest 驱动增强、9P 支持、vsock 支持
- 通过 Windows Update 自动下发

## 6. 图形管线对比

这是架构差异最大的领域，也是决定能否运行 GUI 应用 / Android 的关键。

### 6.1 GPU 虚拟化方案

| 方案 | 原理 | 性能（估算） | 成熟度 |
|------|------|-------------|--------|
| gfxstream | 序列化 GL/VK 命令，通过 virtio-gpu 传输到 host 执行 | ~70-80% | ✅ Cuttlefish / Android Emulator 生产使用 |
| VirGL | GLSL→TGSI→GLSL 双重翻译 | ~50-60% | ✅ 功能完整但性能差 |
| Venus | Vulkan 调用直接转发，SPIR-V 不重编译 | ~80-85% | ✅ 稳定（2023.5） |
| vDRM | 内核驱动层直通，guest 直接用 host GPU 驱动 | ~95% | ⚠️ 开发中 |
| GPU passthrough | 容器直接访问 host GPU | ~100% | ✅ 但只能在容器方案中 |

> 注：性能百分比为基于公开资料的估算值，非实测数据，实际性能因工作负载和硬件差异较大。

### 6.2 各平台图形性能预估

| 平台 | 可行方案 | GPU 性能估算（vs 原生） |
|------|---------|----------------------|
| Linux (KVM) | 容器 GPU 直通（无 VM） | ~95-100% |
| Linux (KVM) | Venus via crosvm | ~80-85% |
| Windows (WHPX) | gfxstream via crosvm | ~70-80% |
| macOS (HVF) | Venus + MoltenVK via libkrun/QEMU | ~55-75% |

> 注：以上性能数据为基于架构分析的粗略估算，非 benchmark 实测。

### 6.3 macOS GPU 的关键限制

Apple Virtualization.framework **只提供 virtio-gpu 2D**，没有 3D 加速。这意味着：
- 使用 VZ framework 无法获得 GPU 加速
- OrbStack 因此完全不做图形（纯 CLI 环境）
- 要图形就必须绕过 VZ，使用 Hypervisor.framework (HVF) + 自己的 VMM

已验证的绕过方案：Podman + libkrun + Venus + MoltenVK，在 Apple Silicon 上成功实现了 Vulkan GPU 加速。

## 7. VMM（Virtual Machine Monitor）对比

| VMM | 语言 | 平台支持 | gfxstream | 安全模型 |
|-----|------|---------|-----------|---------|
| QEMU | C | KVM + WHPX + HVF + 更多 | 社区合并 | 单进程 |
| crosvm | Rust | KVM + WHPX（**无 macOS 支持**） | 原生内置 | 每设备独立沙盒 |
| libkrun | Rust | KVM + HVF | Venus（非 gfxstream） | 嵌入式库 |
| Firecracker | Rust | KVM | 无 | 极简 |
| Apple VZ | — | macOS | 无（仅 2D） | Apple 沙盒 |

> **重要发现：crosvm 官方不支持 macOS。** crosvm 的 hypervisor 后端只有 KVM（Linux）和 WHPX（Windows），HVF（macOS）未在官方支持列表中。crosvm-dev 邮件列表中有人询问过 macOS 移植的可能性，但截至 2026 年 3 月，macOS 支持仍未合入上游。

**crosvm 核心优势：**
- Google 为 Chrome OS 上的 Android/Linux VM 专门打造
- gfxstream 第一方集成，Cuttlefish / Android Emulator 生产验证
- 每个 virtio 设备（gpu、net、blk）运行在独立沙盒进程中

**crosvm 的 macOS 限制：**
- 不支持 macOS HVF，无法直接在 macOS 上使用
- macOS 上需要替代方案：QEMU（支持 HVF）或 libkrun（支持 HVF）

## 8. Apple Virtualization.framework vs Hypervisor.framework

| | Virtualization.framework (VZ) | Hypervisor.framework (HVF) |
|---|---|---|
| 层级 | 高层 API | 底层 API |
| 给你什么 | 完整虚拟机（~50 行代码） | 虚拟 CPU + 虚拟内存（需要自己实现所有设备） |
| GPU | 仅 2D | 完全自由（可接 gfxstream） |
| VirtioFS | 内置 | 需自己实现 |
| 灵活性 | Apple 给什么用什么 | 完全控制 |
| 使用者 | OrbStack, Lima | crosvm, QEMU |

VZ 是黑盒成品，简单但有限制；HVF 是原材料，crosvm 用它构建了完全可控的虚拟机。

## 9. Android 特殊需求分析

### 9.1 为什么 Android 需要共享内核

Android 应用依赖 Binder IPC 与系统服务（ActivityManager、SurfaceFlinger 等）通信。Binder 是内核驱动，只在同一个内核内有效。如果每个 Android 实例使用独立 VM（独立内核），实例内的 App 与系统服务之间的 Binder 通信可以工作，但多实例之间无法共享资源。

### 9.2 binderfs 实现多实例隔离

Linux 5.0+ 引入 binderfs，支持 per-IPC-namespace 挂载：
- 每个容器可以挂载独立的 `/dev/binderfs/`，获得独立的 binder 设备
- 容器间的 binder 通信完全隔离
- 已验证：Anbox Cloud 单机通过 LXC 容器运行 100+ Android 实例

### 9.3 架构翻译

crosvm 自身不支持跨架构模拟（不像 QEMU），但架构翻译在 VM 内部通过 binfmt_misc 解决：

| 场景 | 方案 | 性能 |
|------|------|------|
| x86 .so on Apple Silicon | Rosetta（virtio-fs 挂入 VM） | ~80-90% |
| ARM .so on x86 PC | libndk_translation（Google） | ~60-80% |
| 任意架构兜底 | QEMU user-mode（VM 内部） | ~10-30% |

## 10. 关键结论

1. **共享内核是最优模型** — 多实例边际成本趋近于零，是 OrbStack / WSL 2 成功的核心
2. **crosvm 是最适合的 VMM** — gfxstream 原生集成，安全模型优秀，Cuttlefish / Android Emulator 生产验证
3. **macOS 通过自行移植 crosvm HVF 后端实现三平台统一** — crosvm 官方不支持 macOS，但可行性分析表明移植工作量可控（~2500 行 Rust），crosvm 已有 macOS 平台骨架，gfxstream 已有 macOS/MoltenVK 代码路径。详见 [crosvm HVF 可行性分析](crosvm-hvf-feasibility.md)
4. **gfxstream 是图形管线的首选** — 专为 Android 图形设计，跨平台（含 macOS Metal/MoltenVK），已集成进 crosvm
5. **内核只需自定义配置，不需要改代码** — mainline + virtio + binder/binderfs + namespace/cgroup
6. **底座与上层工作负载解耦** — Linux 发行版和 Android 都只是容器镜像的不同选择
