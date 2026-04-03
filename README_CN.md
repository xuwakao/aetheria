# Aetheria 以太之境

[English](README.md)

跨平台轻量级 Linux 容器运行时，基于 [crosvm](https://github.com/xuwakao/crosvm) VMM。目前支持 macOS（Apple Hypervisor.framework），Linux（KVM）和 Windows（WHPX）支持已规划。

## 功能特性

- **ARM64 Linux 虚拟机** — crosvm 统一 VMM，支持 HVF（macOS）、KVM（Linux）、WHPX（Windows）计划中，秒级就绪
- **容器隔离** — PID/mount/UTS/IPC/network 命名空间 + pivot_root
- **桥接网络** — 每容器 veth pair + nftables NAT（10.42.0.0/24 子网）
- **端口转发** — `-p 8080:80` 通过 vsock 隧道转发流量
- **Cgroups v2** — CPU、内存、进程数限制
- **容器持久化** — VM 重启后自动恢复，支持 `--restart=always`
- **overlayfs 写时复制** — 共享基础镜像，每容器独立可写层
- **交互式 Shell** — PTY over vsock，原始终端模式
- **virtiofs + DAX** — 零拷贝 mmap，接近原生的文件系统共享性能
- **多发行版支持** — Alpine、Ubuntu 24.04、Debian 12

## 快速开始

```bash
# 前置条件：Docker（用于构建 rootfs）、Go 1.21+

# 首次运行 — 自动编译 agent + CLI
./run.sh

# 在另一个终端中：
./run.sh create alpine myapp
./run.sh shell myapp

# 带资源限制和端口转发
./run.sh create ubuntu web -p 8080:80 --memory=512m --cpus=1.0

# VM 启动时自动重启
./run.sh create alpine svc --restart=always

# 列出容器
./run.sh ls

# 停止并删除
./run.sh rm myapp
```

## 架构

```
宿主机（macOS/Linux/Windows）        Linux VM（Alpine，crosvm）
┌─────────────────────┐            ┌──────────────────────────────┐
│ aetheria CLI        │            │ aetheria-agent（PID 1）       │
│   ↕ Unix socket     │            │   ↕ 命名空间隔离              │
│ aetheria daemon     │◄──vsock──►│ ┌────────┐ ┌────────┐        │
│   ↕ crosvm          │            │ │alpine  │ │ubuntu  │ ...    │
│   ↕ virtio-fs/blk   │            │ │(10.42. │ │(10.42. │        │
│   ↕ virtio-net      │            │ │ 0.2)   │ │ 0.3)   │        │
└─────────────────────┘            │ └────────┘ └────────┘        │
                                   │     ↕ br-aetheria + NAT      │
                                   └──────────────────────────────┘
```

## 项目结构

```
aetheria/
├── cmd/aetheria/          # 宿主端 CLI + 守护进程
├── cmd/aetheria-agent/    # 客机端 agent（Linux ARM64）
│   ├── container.go       # 容器生命周期 + 持久化
│   ├── network.go         # 桥接网络 + nftables
│   ├── cgroup.go          # Cgroups v2 资源限制
│   ├── portforward.go     # 端口转发（vsock 隧道）
│   ├── images.go          # 发行版镜像管理 + overlayfs
│   ├── shell.go           # 交互式 Shell RPC
│   └── pty.go             # PTY 分配
├── aetheria-crosvm/       # crosvm fork + HVF 后端（子模块）
├── aetheria-kernel/       # 自定义 Linux 6.12.15 内核（子模块）
├── run.sh                 # 一键启动脚本
└── docs/                  # 设计文档 + 状态报告
```

## 命令列表

| 命令 | 说明 |
|------|------|
| `run` | 启动 VM 守护进程 |
| `create <发行版> [名称]` | 创建并启动容器 |
| `shell <名称>` | 进入交互式 Shell |
| `exec <命令>` | 在 VM 中执行命令 |
| `ls` | 列出所有容器 |
| `rm <名称>` | 停止并删除容器 |
| `pull <发行版>` | 下载发行版镜像 |
| `images` | 列出可用镜像 |
| `ping` | 健康检查 |
| `stop` | 关闭 VM |

### 创建选项

```
-p 宿主端口:容器端口       端口转发（可重复）
--net=bridge|host|none    网络模式（默认：bridge）
--memory=512m             内存限制
--cpus=1.0                CPU 限制
--pids=1024               最大进程数
--restart=always          VM 启动时自动重启
```

## 许可证

MIT
