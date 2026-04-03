# Aetheria 以太之境

[English](README.md)

跨平台轻量级 Linux 容器运行时，基于 [crosvm](https://github.com/xuwakao/crosvm)（fork 自 [Google crosvm](https://chromium.googlesource.com/crosvm/crosvm)）VMM。目前支持 macOS（Apple Hypervisor.framework），Linux（KVM）和 Windows（WHPX）支持已规划。

## 为什么做 Aetheria？

在非 Linux 系统上运行 Linux 容器需要虚拟机。现有方案各有不足：

| 方案 | 局限性 |
|------|--------|
| Docker Desktop | 体积大（~2GB）、闭源、商业许可限制 |
| OrbStack | 仅 macOS、闭源 |
| Lima/colima | 基于 QEMU、无 GPU 加速路径 |
| WSL2 | 仅 Windows、微软控制内核 |
| Podman Machine | 基于 QEMU、macOS 集成有限 |

**Aetheria** 的目标是一个开源、跨平台的替代方案：

- **统一 VMM** — crosvm 一套代码支持 HVF（macOS）、KVM（Linux）、WHPX（Windows）
- **接近原生 I/O** — virtiofs DAX 零拷贝 mmap，读吞吐量达 30 GB/s
- **轻量** — 基于 Alpine 的 VM 约 5 秒启动，空闲内存占用 ~30MB
- **GPU 路径** — crosvm 的 virtio-gpu + Rutabaga 为 3D 加速提供路线图

## 功能特性

- **容器隔离** — PID/mount/UTS/IPC/network 命名空间 + pivot_root
- **桥接网络** — 每容器 veth pair + nftables NAT（10.42.0.0/24 子网）
- **端口转发** — `-p 8080:80` 通过 vsock 隧道转发流量
- **Cgroups v2** — CPU、内存、进程数限制
- **容器持久化** — VM 重启后自动恢复，支持 `--restart=always`
- **overlayfs 写时复制** — 共享基础镜像，每容器独立可写层
- **交互式 Shell** — PTY over vsock，原始终端模式
- **virtiofs + DAX** — 零拷贝 mmap，接近原生的文件系统共享性能
- **多发行版支持** — Alpine 3.21、Ubuntu 24.04、Debian 12

## 架构

```
宿主机（macOS / Linux / Windows）
┌──────────────────────────────────────────────────────┐
│  aetheria CLI + daemon (Go)                          │
│    → 管理 crosvm 进程                                 │
│    → 通过 vsock 与 guest 通信                         │
│                                                      │
│  crosvm (Rust VMM, 跨平台统一):                       │
│    macOS:   HVF (Apple Hypervisor.framework)         │
│    Linux:   KVM                                      │
│    Windows: WHPX                                     │
│  设备: virtio-blk, virtio-net, virtio-fs,             │
│        virtio-vsock, virtio-gpu (Rutabaga)           │
│                                                      │
│══════════════════ VM 边界 ═══════════════════════════│
│                                                      │
│  单 Linux VM（共享内核模型）                            │
│    自定义内核 (mainline 6.12 + virtio + ns/cgroup)    │
│    aetheria-agent (Go, vsock JSON-RPC 服务)           │
│                                                      │
│    ┌────────────┐ ┌────────────┐ ┌────────────┐     │
│    │ 容器 1     │ │ 容器 2     │ │ 容器 N     │     │
│    │ Alpine     │ │ Ubuntu     │ │  ...       │     │
│    │ ns+cgroup  │ │ ns+cgroup  │ │ ns+cgroup  │     │
│    │ overlayfs  │ │ overlayfs  │ │ overlayfs  │     │
│    │ 10.42.0.2  │ │ 10.42.0.3  │ │ 10.42.0.x  │     │
│    └─────┬──────┘ └─────┬──────┘ └─────┬──────┘     │
│          └──────┬──br-aetheria──┬──────┘             │
│                 │  10.42.0.1/24 │                    │
│              nftables NAT masquerade                 │
│                      ↕                               │
│                 eth0 → 互联网                         │
└──────────────────────────────────────────────────────┘
```

### 通信通道

| 通道 | 传输方式 | 用途 |
|------|---------|------|
| 控制 RPC | vsock 端口 1024 | CLI → daemon → agent，JSON-RPC（create/start/stop/exec/...） |
| Shell 流 | vsock 端口 1025 | 双向 PTY 字节流，原始终端模式 |
| 端口转发 | vsock 端口 1026 | 每连接 TCP 隧道，基于 header 的多路复用 |
| 文件系统 | virtio-fs + DAX | 宿主目录共享，通过 `hv_vm_map` 零拷贝 mmap |
| 块存储 | virtio-blk | 根磁盘 (ext4) + 数据盘 (64GB 稀疏，精简配置) |

## 快速开始

### 前置条件

- Go 1.21+
- Rust 工具链（编译 crosvm）
- Docker（构建 ARM64 rootfs）
- Apple Silicon Mac（当前支持平台）

### 构建

```bash
# 克隆（含子模块）
git clone --recursive https://github.com/xuwakao/aetheria.git
cd aetheria

# 1. 编译 crosvm（首次，约 5 分钟）
cd aetheria-crosvm
cargo build --release
cd ..

# 2. 编译内核（首次，约 10 分钟）
cd aetheria-kernel
./build-kernel.sh arm64
./build-rootfs.sh arm64
cd ..

# 3. 编译 agent + CLI + 安装到 rootfs（首次 ./run.sh 自动执行）
./run.sh build
```

### 运行

```bash
# 终端 1：启动 VM 守护进程
./run.sh

# 终端 2：创建并进入容器
./run.sh create alpine myapp
./run.sh shell myapp

# 带资源限制和端口转发
./run.sh create ubuntu web -p 8080:80 --memory=512m --cpus=1.0

# VM 启动时自动重启
./run.sh create alpine svc --restart=always

# 列出 / 删除
./run.sh ls
./run.sh rm myapp
```

## 命令列表

| 命令 | 说明 |
|------|------|
| `run` | 启动 VM 守护进程（前台运行） |
| `create <发行版> [名称]` | 创建并启动容器 |
| `shell <名称>` | 容器内交互式 Shell |
| `exec <命令>` | 在 VM 中执行命令 |
| `ls` | 列出所有容器 |
| `rm <名称>` | 停止并删除容器 |
| `pull <发行版>` | 下载发行版镜像 |
| `images` | 列出可用/已缓存镜像 |
| `ping` | Agent 健康检查 |
| `info` | 显示 VM 信息 |
| `stop` | 关闭 VM |

### 创建选项

```
-p 宿主端口:容器端口       端口转发（可重复）
--net=bridge|host|none    网络模式（默认：bridge）
--memory=512m             内存限制（支持 k/m/g）
--cpus=1.0                CPU 限制（支持小数）
--pids=1024               最大进程数
--restart=always          VM 启动时自动重启
```

## 性能

测试环境：Apple M 系列芯片，crosvm/HVF，Linux 6.12.15：

| 指标 | Aetheria | QEMU virtiofsd | 提升 |
|------|----------|----------------|------|
| virtiofs 读 (DAX mmap, 缓存) | 30 GB/s | 640 MB/s | **47x** |
| virtiofs 4K 写 | 55 MB/s | 14 MB/s | **4x** |
| virtio-blk 顺序读 | 22.5 GB/s | — | — |
| VM 启动到 shell | ~5.5 秒 | — | — |

**关键优化：**
- MAP_SHARED DAX — guest mmap 通过 `hv_vm_map` 直接访问宿主文件页，零拷贝
- Per-inode DAX (FUSE_ATTR_DAX) — 逐文件粒度的 DAX 控制
- FSEvents 自适应缓存 — macOS 文件系统变更实时失效 FUSE 缓存
- Writeback caching + 中断合并 + 4MB FUSE 缓冲区

## 项目结构

```
aetheria/
├── cmd/aetheria/            # 宿主端 CLI + 守护进程
├── cmd/aetheria-agent/      # 客机端 agent（Linux ARM64）
│   ├── container.go         #   容器生命周期 + 持久化
│   ├── network.go           #   桥接网络 + nftables NAT
│   ├── cgroup.go            #   Cgroups v2 资源限制
│   ├── portforward.go       #   端口转发（vsock 隧道）
│   ├── images.go            #   发行版镜像管理 + overlayfs
│   ├── shell.go             #   交互式 Shell RPC 处理
│   └── pty.go               #   PTY 分配 + nsenter
├── aetheria-crosvm/         # crosvm fork — 新增 macOS HVF 后端（子模块）
├── aetheria-kernel/         # 自定义 Linux 6.12.15 内核配置（子模块）
├── run.sh                   # 一键启动脚本
├── scripts/                 # 构建和安装辅助脚本
└── docs/                    # 设计文档、计划、状态报告
```

## 路线图

- [x] crosvm HVF 后端（Apple Hypervisor.framework）
- [x] 自定义内核（Linux 6.12.15，ARM64 + x86_64）
- [x] virtiofs + DAX 零拷贝文件共享
- [x] 容器运行时（命名空间 + overlayfs）
- [x] 桥接网络 + nftables NAT
- [x] 端口转发（`-p host:container`）
- [x] Cgroups v2 资源隔离
- [x] 容器持久化 + 自动重启
- [ ] OCI 镜像支持（Docker Hub pull）
- [ ] AetheriaDisplay.app（Metal GPU 渲染）
- [ ] 卷挂载（`-v host:container`）
- [ ] Linux KVM 后端
- [ ] Windows WHPX 后端

## 致谢

- [crosvm](https://chromium.googlesource.com/crosvm/crosvm) — Google Chrome OS 虚拟机监控器
- [Linux kernel](https://kernel.org) — 为轻量 VM 定制配置
- [Alpine Linux](https://alpinelinux.org) — 最小化客机操作系统

## 许可证

[MIT](LICENSE) — 再分发时需保留版权声明。
