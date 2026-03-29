# Aetheria 架构设计

## 1. 项目定位

Aetheria（以太之境）是一个跨平台的轻量级 Linux 容器运行环境。

- 类似 OrbStack，但支持 macOS、Windows、Linux 三平台
- 共享内核架构，多容器实例边际成本趋近于零
- 使用 crosvm 作为 VMM，支持 GPU 加速（gfxstream）
- 底座与上层解耦——Linux 发行版、Android 等都只是容器镜像的不同选择

## 2. 整体架构

```
┌──────────────────────────────────────────────────────────────┐
│  Host (macOS / Windows / Linux)                              │
│                                                              │
│  ┌──────────────┐                                            │
│  │  aetheria CLI │  Go, 用户交互入口                           │
│  │  (跨平台)     │  aetheria create / start / stop / list     │
│  └──────┬───────┘                                            │
│         │                                                    │
│  ┌──────▼───────┐     ┌───────────────────────────┐          │
│  │ aetheria      │────→│  crosvm (VMM, Rust)       │          │
│  │ daemon (Go)   │     │                           │          │
│  │               │     │  Linux:   KVM             │          │
│  │ 管理 VM 生命周期│     │  Windows: WHPX            │          │
│  │ 管理实例配置   │     │  macOS:   HVF (自行移植)   │          │
│  └──────┬───────┘     │                           │          │
│         │ vsock       │  virtio 设备:              │          │
│         │             │  ├── virtio-blk  (磁盘)    │          │
│         │             │  ├── virtio-net  (网络)     │          │
│         │             │  ├── virtio-fs   (文件共享)  │          │
│         │             │  ├── virtio-vsock (通信)    │          │
│         │             │  └── virtio-gpu  (图形)     │──→ Host GPU
│         │             └───────────────────────────┘          │
│  ═══════╪════════════════════════════════════════             │
│         │              VM 边界                                │
│  ┌──────▼────────────────────────────────────────────────┐   │
│  │  Linux VM                                             │   │
│  │                                                       │   │
│  │  ┌─────────────────────────────────┐                  │   │
│  │  │  定制内核                        │                  │   │
│  │  │  mainline + virtio + binder     │                  │   │
│  │  │  + binderfs + namespace/cgroup  │                  │   │
│  │  └─────────────────────────────────┘                  │   │
│  │                                                       │   │
│  │  ┌─────────────────────────────────┐                  │   │
│  │  │  aetheria-agent (Go, PID 1)     │                  │   │
│  │  │  vsock gRPC server              │                  │   │
│  │  │                                 │                  │   │
│  │  │  职责:                           │                  │   │
│  │  │  ├── 接收 host daemon 指令       │                  │   │
│  │  │  ├── 管理 nspawn/LXC 容器        │                  │   │
│  │  │  ├── 管理网络 (bridge/veth)      │                  │   │
│  │  │  ├── 管理 binderfs 分配          │                  │   │
│  │  │  └── 管理文件挂载               │                  │   │
│  │  └──────────┬──────────────────────┘                  │   │
│  │             │                                         │   │
│  │  ┌──────────▼──────────┐  ┌─────────────────────┐    │   │
│  │  │  nspawn 容器 1       │  │  nspawn 容器 2      │    │   │
│  │  │  Ubuntu / Fedora /  │  │  Arch / Android /   │    │   │
│  │  │  任意 Linux 发行版   │  │  任意工作负载        │    │   │
│  │  │                     │  │                     │    │   │
│  │  │  独立 PID namespace  │  │  独立 PID namespace  │    │   │
│  │  │  独立 NET namespace  │  │  独立 NET namespace  │    │   │
│  │  │  独立 MNT namespace  │  │  独立 MNT namespace  │    │   │
│  │  │  独立 IPC namespace  │  │  独立 IPC namespace  │    │   │
│  │  └─────────────────────┘  └─────────────────────┘    │   │
│  └───────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────┘
```

## 3. 分层设计

### 3.1 第一层：CLI + Daemon（Host 侧，Go）

**aetheria CLI** 是用户的直接入口：

```bash
aetheria create ubuntu my-dev       # 创建 Ubuntu 实例
aetheria create fedora my-server    # 创建 Fedora 实例
aetheria start my-dev               # 启动实例
aetheria stop my-dev                # 停止实例
aetheria shell my-dev               # 进入实例 shell
aetheria list                       # 列出所有实例
aetheria delete my-dev              # 删除实例
```

**aetheria daemon** 是后台常驻服务：

- 管理 crosvm 进程的生命周期
- 维护实例配置和状态
- 通过 vsock 与 VM 内的 agent 通信
- VM 懒启动——没有实例运行时不启动 VM

**数据流：**
```
CLI → (本地 socket/RPC) → Daemon → (管理 crosvm 进程)
                                  → (vsock gRPC) → Agent
```

### 3.2 第二层：crosvm（VMM，三平台统一）

crosvm 是 Google 开发的 Rust VMM，负责创建和维持唯一的一个 Linux VM。**三个平台统一使用 crosvm**，macOS 通过自行移植 HVF 后端实现。

**平台 Hypervisor 后端：**

| 平台 | Hypervisor | crosvm 支持状态 |
|------|-----------|---------------|
| Linux | KVM | ✅ 官方支持（主力平台） |
| Windows | WHPX | ✅ 官方支持 |
| macOS | HVF (Hypervisor.framework) | ⚠️ 需自行移植（详见 [crosvm HVF 可行性分析](research/crosvm-hvf-feasibility.md)） |

**为什么三平台统一用 crosvm：**
- gfxstream 原生集成——GPU 图形管线现成，且 gfxstream 已有完整的 macOS/MoltenVK 代码路径
- guest 侧完全一致——无论哪个平台，容器看到的都是相同的 virtio 设备
- 避免维护 crosvm + QEMU 两套 VMM 的复杂度
- crosvm 已有 macOS 平台抽象层骨架（event/kqueue/timer 已实现），移植基础已就绪

**为什么 macOS 不能用 Apple VZ framework：** Virtualization.framework 只提供 virtio-gpu 2D，不支持 3D GPU 加速。

**macOS HVF 后端移植要点：**
- 实现 crosvm 的 Hypervisor/Vm/Vcpu trait，调用 Apple Hypervisor.framework C API
- 估计 ~2000-2800 行 Rust（ARM64 exit 处理比 x86 简单，QEMU HVF 可参考）
- crosvm 已有 33 个 macOS 平台 `todo!()` 待补齐（~500-800 行，多为 POSIX 映射）
- 沙盒先禁用（Windows 后端同样无沙盒），不影响 VM/容器隔离
- 详细分析见 [crosvm HVF 可行性分析](research/crosvm-hvf-feasibility.md)

**crosvm 选择理由：**
- 为 Android/Linux VM 场景专门设计（Cuttlefish / Android Emulator 生产验证）
- gfxstream 原生集成——GPU 图形管线现成
- 每个 virtio 设备运行在独立沙盒进程中——安全（Linux 平台）
- Rust 编写，内存安全

**crosvm 提供的虚拟设备（三平台统一）：**

| 设备 | 用途 |
|------|------|
| virtio-blk | VM 磁盘（rootfs + 数据盘） |
| virtio-net | VM 网络（NAT） |
| virtio-fs | Host↔Guest 文件共享 |
| virtio-vsock | Host↔Guest 通信通道 |
| virtio-gpu + gfxstream | GPU 加速图形 |

### 3.3 第三层：Linux 内核（VM 内，共享）

所有容器实例共享一个定制 Linux 内核。不修改内核源码，只自定义 `.config`。

**必需的内核配置：**

```kconfig
# Virtio 设备（对应 crosvm 虚拟设备）
CONFIG_VIRTIO_BLK=y
CONFIG_VIRTIO_NET=y
CONFIG_VIRTIO_FS=y
CONFIG_VIRTIO_VSOCKETS=y
CONFIG_DRM_VIRTIO_GPU=y

# 容器隔离
CONFIG_NAMESPACES=y
CONFIG_USER_NS=y
CONFIG_PID_NS=y
CONFIG_NET_NS=y
CONFIG_CGROUPS=y
CONFIG_OVERLAY_FS=y

# Android 支持（为未来 Android 容器预留）
CONFIG_ANDROID_BINDER_IPC=y
CONFIG_ANDROID_BINDERFS=y

# 文件系统
CONFIG_EXT4_FS=y
CONFIG_FUSE_FS=y

# 网络
CONFIG_BRIDGE=y
CONFIG_VETH=y
CONFIG_NETFILTER=y
```

**内核策略：**
- 基于 mainline Linux LTS 分支
- 初期可使用 Kata Containers 预编译内核快速启动
- 后期维护自己的 defconfig，不 fork 内核源码

### 3.4 第四层：aetheria-agent（VM 内，Go，PID 1）

agent 是 VM 内部的核心管理进程，替代 systemd 作为 PID 1。

**通信协议：** vsock + gRPC（端口 1024）

**核心职责：**

```
daemon: "创建实例 my-dev，使用 Ubuntu 镜像"
agent:
  1. 创建实例目录 /instances/my-dev/
  2. 准备 rootfs（overlay mount，基于 Ubuntu base image）
  3. 分配 veth pair，接入网桥，分配 IP
  4. 启动 systemd-nspawn 容器
  5. 容器内运行 systemd → 完整的 Ubuntu 环境
  6. 上报状态给 daemon

daemon: "停止实例 my-dev"
agent:
  1. 向 nspawn 进程发送 SIGTERM
  2. 清理网络（veth、IP）
  3. 卸载文件系统
  4. 上报状态

daemon: "列出所有实例"
agent:
  1. 遍历 /instances/，收集每个容器状态
  2. 返回实例列表（名称、状态、IP、资源使用）
```

### 3.5 第五层：容器实例（nspawn/LXC）

每个实例是 VM 内的一个 systemd-nspawn 容器。

**实例看到的环境：**
```
/                         ← 独立 rootfs (overlay mount)
/dev/                     ← 独立设备视图
独立 PID namespace         → PID 1 是容器内的 init
独立 NET namespace         → 有自己的 veth + IP 地址
独立 MNT namespace         → 有自己的文件系统视图
独立 IPC namespace         → 独立的 IPC 资源
共享内核                   → 所有容器使用同一个内核
```

**对于 Android 实例（未来）：**
```
额外配置：
  /dev/binderfs/           ← 独立的 binderfs 挂载
  /dev/binderfs/binder     ← 独立 binder 设备
  /dev/binderfs/hwbinder
  /dev/binderfs/vndbinder

进程树：
  PID 1: /init (Android)
  PID 2: servicemanager
  PID 3: zygote
  PID 4: system_server
  PID 5: surfaceflinger → virtio-gpu → crosvm → host GPU
  PID N: 用户 App
```

## 4. 图形管线

### 4.1 gfxstream 渲染路径

```
容器内应用渲染一帧:
  App → OpenGL ES / Vulkan
    → gfxstream guest 驱动（序列化 GPU 命令）
    → virtio-gpu virtio ring
    → crosvm host 侧
    → gfxstream host 库（反序列化）
    → 调用 host GPU API
    → 渲染到 host 窗口

各平台 host 侧 GPU API:
  Linux:   Vulkan → GPU（直接）
  Windows: Vulkan / D3D12 → GPU
  macOS:   MoltenVK → Metal → Apple GPU
```

### 4.2 架构翻译（VM 内部）

crosvm 不支持跨 CPU 架构模拟，但架构翻译在 VM 内部通过 binfmt_misc 透明处理：

| 平台 | VM 架构 | 翻译方案 | 性能 |
|------|--------|---------|------|
| Apple Silicon Mac | ARM64 | Rosetta（virtio-fs 挂入） | ~80-90% |
| x86 PC | x86_64 | libndk_translation / QEMU user-mode | ~60-80% / ~10-30% |

翻译层对 crosvm 完全透明——crosvm 只提供 virtio-fs 和标准 Linux VM 环境，翻译注册和执行由 guest 内核的 binfmt_misc 处理。

## 5. 网络架构

### 5.1 VM 内部网络

```
VM 内部:
  aetheria-agent
    └── 管理 Linux bridge (br0)
        ├── 容器 1: veth1 ←→ br0  (192.168.100.2)
        ├── 容器 2: veth2 ←→ br0  (192.168.100.3)
        └── 容器 N: vethN ←→ br0  (192.168.100.N+1)
```

### 5.2 VM ↔ Host 网络

```
容器 → veth → bridge → virtio-net → crosvm NAT → Host 网络 → 外网
```

### 5.3 Host ↔ Guest 通信

```
非网络通道:
  daemon ←→ agent: vsock + gRPC（不走 TCP/IP，更快更可靠）

网络通道:
  端口转发: 容器内 0.0.0.0:8080 → host localhost:8080
  域名访问: instance-name.aetheria.local（可选）
```

## 6. 文件共享

```
VirtioFS:
  Host 目录 → crosvm virtio-fs 设备 → VM 内挂载

  Host: /Users/alice/projects/
  VM:   /mnt/host/Users/alice/projects/
  容器: /mnt/host/Users/alice/projects/ (bind mount)
```

## 7. 仓库结构

### aetheria-crosvm（crosvm fork，增加 HVF 后端）

```
aetheria-crosvm/
├── hypervisor/src/
│   ├── hvf/                # 新增：HVF 后端
│   │   ├── mod.rs            Hypervisor + Vm trait 实现
│   │   ├── vcpu.rs           Vcpu trait，run loop，exit 翻译
│   │   ├── types.rs          HVF 类型定义
│   │   └── ffi.rs            Apple HVF C API 的 Rust 绑定
│   ├── kvm/                # 已有
│   ├── whpx/               # 已有
│   └── ...
├── base/src/sys/macos/     # 补齐已有的 todo!() 桩函数
└── ...                     # 其余与上游 crosvm 同步
```

### aetheria（主仓库）

```
aetheria/
├── cmd/
│   ├── aetheria/           # CLI 入口
│   └── aetheria-agent/     # Guest agent 入口
├── internal/
│   ├── daemon/             # Daemon 核心逻辑
│   ├── agent/              # Agent 核心逻辑
│   ├── crosvm/             # crosvm 进程管理
│   ├── machine/            # 实例管理（创建/删除/启停）
│   ├── network/            # 网络管理
│   ├── storage/            # 存储管理
│   └── api/                # gRPC 生成代码
├── proto/                  # .proto 文件
├── docs/                   # 文档
├── aetheria-crosvm/        # submodule → github.com/xuwakao/crosvm
├── aetheria-kernel/        # submodule → github.com/xuwakao/aetheria-kernel
├── aetheria-forge/         # submodule → github.com/xuwakao/aetheria-forge
└── go.mod
```

### aetheria-kernel（内核仓库）

```
aetheria-kernel/
├── configs/
│   ├── aetheria_defconfig  # 主 defconfig
│   └── fragments/          # 模块化 config fragments
│       ├── virtio.config
│       ├── android.config
│       ├── namespace.config
│       └── minimal.config
├── patches/                # 如有必要的补丁（尽量避免）
├── scripts/
│   └── build-kernel.sh     # 内核构建脚本
└── README.md
```

### aetheria-forge（镜像构建仓库）

```
aetheria-forge/
├── rootfs/
│   ├── base/               # 基础 rootfs 构建脚本
│   └── overlay/            # agent 等额外文件
├── images/
│   ├── ubuntu/             # Ubuntu 容器镜像构建
│   ├── fedora/             # Fedora 容器镜像构建
│   └── alpine/             # Alpine 容器镜像构建
├── scripts/
│   ├── build-rootfs.sh     # rootfs 构建
│   ├── build-image.sh      # 最终镜像打包
│   └── download-kernel.sh  # 下载预编译内核
├── ci/                     # CI/CD 配置
└── README.md
```

## 8. 性能目标

| 指标 | 目标值 | 参考 |
|------|-------|------|
| VM 冷启动 | < 3 秒 | OrbStack ~2 秒 |
| 新建容器实例 | < 1 秒 | OrbStack 瞬间 |
| CPU 性能（vs 原生） | > 95% | 硬件虚拟化 |
| 内存性能（vs 原生） | > 95% | EPT/NPT |
| 磁盘 I/O（vs 原生） | > 90% | virtio-blk |
| 网络吞吐 | > 10 Gbps | OrbStack 45Gbps |
| GPU 性能（vs 原生） | > 70% | gfxstream |
| 空闲实例开销 | ~0 CPU | 共享内核 + nspawn |

## 9. 技术栈总结

| 组件 | 技术选型 | 理由 |
|------|---------|------|
| Host CLI / Daemon | Go | 跨平台编译、生态成熟、Lima/Colima/Podman 验证 |
| Guest Agent | Go | 与 host 共享 protobuf、类型定义 |
| VMM (全平台) | crosvm (Rust) | 三平台统一；gfxstream 原生集成；macOS 需自行移植 HVF 后端 |
| Host↔Guest 通信 | vsock + gRPC | 全平台统一、不走网络协议栈 |
| 容器隔离 | systemd-nspawn / LXC | 成熟、支持 systemd、binderfs 兼容 |
| 图形 | gfxstream over virtio-gpu | 为 Android 设计、跨平台 |
| 内核 | mainline Linux LTS + 自定义 config | 不 fork 源码、只定制配置 |
| 文件共享 | VirtioFS | crosvm 原生支持 |
| macOS Hypervisor | HVF (via crosvm，自行移植) | VZ 无 GPU 3D；HVF 后端需移植，可行性已验证 |
| Windows Hypervisor | WHPX (via crosvm) | Windows Hypervisor Platform |
| Linux Hypervisor | KVM (via crosvm) | 标准选择 |
