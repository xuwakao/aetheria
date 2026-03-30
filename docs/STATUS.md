# Aetheria 项目状态报告

最后更新: 2026-03-31

## 一、架构回顾

### 设计目标（调研阶段）

```
Host (macOS/Windows/Linux)
  aetheria CLI + daemon (Go)
    → 管理 crosvm 进程
    → 通过 vsock 与 guest 通信

  crosvm (Rust VMM, 跨平台统一):
    Linux:   KVM
    Windows: WHPX
    macOS:   HVF (自研移植)
  设备: virtio-blk, virtio-net, virtio-fs, virtio-vsock, virtio-gpu+gfxstream

  ══════ VM 边界 ══════

  单 Linux VM (共享内核模型)
    自定义内核 (mainline + binder/binderfs + virtio + namespace/cgroup)
    aetheria-agent (Go, PID 1, vsock gRPC 服务)
    nspawn 容器 1: Ubuntu
    nspawn 容器 N: Android (未来)
```

### 关键架构决策

| 决策 | 理由 | 当前状态 |
|------|------|----------|
| crosvm 统一 VMM | 跨平台一致、gfxstream 原生支持 | macOS HVF 已实现 |
| 自研 HVF 移植 | VZ framework 无 3D GPU | 已完成，1,694 行 Rust |
| 共享内核 + nspawn | OrbStack 验证的模型，边际成本近零 | 内核已构建，容器层未开始 |
| Go 宿主端 | 跨编译简单、生态成熟 | 仅 placeholder |
| vsock + gRPC | 宿主↔客机通信 | virtio-vsock 未实现 |
| gfxstream 图形 | macOS Metal 通路已验证 | 未开始 |

---

## 二、仓库总览

| 仓库 | 规划用途 | 实际状态 |
|------|----------|----------|
| **aetheria** | Go CLI + daemon + agent + proto | 14 行 Go (两个空 main.go)，目前实际承载 docs + submodule 管理 |
| **aetheria-crosvm** | crosvm fork + HVF 后端 | **核心工作区**，7,568 行 macOS 新增代码，VM 可启动到交互式 shell |
| **aetheria-kernel** | 自定义 defconfig + 构建脚本 | 已完成，Linux 6.12.15 可启动，ARM64 + x86_64 defconfig |
| **aetheria-forge** | 镜像构建工具 | 空仓库（仅 README） |

---

## 三、各模块详细状态

### 3.1 Hypervisor — HVF 后端 (1,694 行)

**设计预期**: ~2,500 行 Rust，3-5 个月。
**实际**: 1,694 行，集中在约 2 天内完成。低于预期行数是因为 dlsym 动态绑定减少了重复代码。

| 文件 | 行数 | 功能 | 完成度 |
|------|------|------|--------|
| `ffi.rs` | 516 | HVF API FFI 绑定，dlsym 运行时检测 macOS 14/15 | 完整 |
| `vm.rs` | 390 | VM 创建、guest 内存映射、native GIC 创建 | 完整 |
| `vcpu.rs` | 558 | vCPU 运行循环、系统寄存器陷入、MMIO/HVC 处理 | 完整（单 vCPU） |
| `mod.rs` | 70 | Hvf hypervisor 入口 | 完整 |
| `tests.rs` | 160 | 单元测试 | 基础覆盖 |

**与架构设计的偏差**:
- 预期 "~2500 行"，实际 1,694 行 — 比预期更精简
- 预期 HVF 只做 vCPU + 内存，实际还包含了 GIC native API 集成（macOS 15 的 hv_gic_* 系列），这是调研时未预见的
- 单 vCPU 限制（ISS-004）：HVF 的 vCPU 线程亲和性要求导致当前只支持 1 个 vCPU，需要架构调整才能支持多核

### 3.2 GIC 中断控制器 (614 行)

**设计预期**: 调研阶段未单独规划，预期由 HVF 或 crosvm 已有代码处理。
**实际**: 需要完全自研。

| 文件 | 行数 | 功能 |
|------|------|------|
| `hvf_gic.rs` | 380 | IRQ 芯片实现，native GIC (macOS 15+) + 软件回退 (macOS 14) |
| `hvf_gic_mmio.rs` | 234 | 软件 GICD/GICR MMIO 模拟（macOS 14 回退路径） |

**关键发现**: macOS 15 引入了 `hv_gic_create` / `hv_gic_set_spi` 等 native GIC API，大幅简化了中断处理。macOS 14 需要完整的软件 GIC 模拟。两条路径都已实现并工作。

### 3.3 PSCI 电源管理 (169 行)

**设计预期**: 调研阶段未涉及。
**实际**: Linux 内核通过 PSCI 协议管理 CPU 电源状态，在 HVF 上必须由 userspace 模拟。

| 文件 | 行数 | 功能 |
|------|------|------|
| `psci.rs` | 169 | PsciDevice — hypercall bus 设备，处理 PSCI 1.0 全部调用 |

已正确实现为 bus 设备（与 SmcccTrng 同级），非内联代码。SYSTEM_OFF/RESET 通过原子标志通知 vCPU 循环退出。

### 3.4 平台基础库 — base/sys/macos (2,565 行)

**设计预期**: 调研时发现 "crosvm 已有 macOS 平台骨架（event/kqueue 已实现）"。
**实际**: 骨架确实存在，但大量 `todo!()` 需要补全，最终 2,565 行。

| 文件 | 行数 | 功能 | 状态 |
|------|------|------|------|
| `mod.rs` | 614 | kqueue EventContext, fallocate, shm, CPU 信息 | 完整 |
| `mmap.rs` | 1,192 | 内存映射 (msync, madvise) | 完整（上游已有） |
| `event.rs` | 96 | kqueue EVFILT_USER 事件 | 完整 |
| `kqueue.rs` | 120 | kqueue 底层封装 | 完整 |
| `timer.rs` | 89 | kqueue EVFILT_TIMER | 完整 |
| `net.rs` | 212 | Unix socket, SOCK_STREAM 替代 SEQPACKET | 完整 |
| `ioctl_macros.rs` | 112 | BSD 风格 ioctl 编码 | 完整（上游已有） |
| `terminal.rs` | 65 | POSIX termios 终端原始模式 | 完整 |
| `file_traits.rs` | 44 | F_PREALLOCATE 文件分配 | 完整 |
| `notifiers.rs` | 21 | 读/关闭通知器 | 完整 |

**与设计的一致性**: 完全符合预期。kqueue 作为 epoll 的 macOS 等价物，所有底层原语工作正常。

### 3.5 异步执行器 — cros_async/sys/macos (947 行)

**设计预期**: 调研阶段未单独提及。
**实际**: 全部 stub 需要替换为真实 kqueue reactor 实现。

| 文件 | 行数 | 功能 | 状态 |
|------|------|------|------|
| `kqueue_reactor.rs` | 438 | kqueue 异步 reactor（等价 Linux epoll/io_uring） | 完整 |
| `kqueue_source.rs` | 342 | 异步 I/O 源，pread/pwrite + wait | 完整 |
| `async_types.rs` | 39 | AsyncTube, AsyncBufReader 等 | 完整 |
| `event.rs` | 35 | 事件 future | 完整 |
| `timer.rs` | 27 | 定时器 future | 完整 |
| `executor.rs` | 16 | ExecutorKindSys::Kqueue | 完整 |
| `error.rs` | 18 | 错误映射 | 最小实现 |

**关键**: 这是 virtio 设备异步 I/O 的基础。57 个 cros_async 测试通过。

### 3.6 串口 / 终端 (264 + 65 行)

| 文件 | 功能 | 状态 |
|------|------|------|
| `devices/src/sys/macos/serial_device.rs` | 串口设备创建 | 完整 |
| `base/src/sys/macos/terminal.rs` | stdin 原始模式 | 完整 |
| `arch/src/serial/sys/macos.rs` | 串口设备无沙箱创建 | 完整 |

**结果**: 8250/16550A UART 工作，earlycon + ttyS0 控制台输出正常，交互式 shell 可用。

### 3.7 主运行入口 — run_config (575 行)

`src/crosvm/sys/macos.rs` 是 macOS VM 的主入口：

| 功能 | 状态 |
|------|------|
| VM 组件构建 (VmComponents) | 完整 |
| Guest 内存创建 | 完整（简化版，ISS-015） |
| Arch::build_vm 调用 | 完整 |
| GIC MMIO 回退注册 | 完整 |
| IRQ 处理线程 | 完整 |
| vCPU 循环 | 完整（单 vCPU） |
| PSCI 设备注册 | 完整 |
| 终端原始模式 | 完整 |
| virtio-blk 设备注册 | **未实现** |
| virtio-net 设备注册 | **未实现** |
| virtio-9p 设备注册 | **未实现** |
| virtio-vsock 设备注册 | **未实现** |
| VM 控制套接字 | **未实现**（ISS-011） |

### 3.8 内核 — aetheria-kernel

**设计预期**: mainline Linux，仅定制 .config，不修改源码。
**实际**: 完全符合。

| 项目 | 状态 |
|------|------|
| 内核版本 | Linux 6.12.15 (LTS) |
| 构建方式 | Docker 交叉编译 (gcc:14) |
| defconfig | ARM64 + x86_64 两份，265/248 行 |
| 启动时间 | ~60ms (HVF, 256MB RAM) |

**defconfig 与架构设计对比**:

| 规划的配置 | defconfig 中 | 状态 |
|------------|-------------|------|
| CONFIG_VIRTIO_BLK | 有 | 已就绪 |
| CONFIG_VIRTIO_NET | 有 | 已就绪 |
| CONFIG_VIRTIO_FS | 有 | 已就绪 |
| CONFIG_VIRTIO_VSOCKETS | 有 | 已就绪 |
| CONFIG_9P_FS + NET_9P_VIRTIO | 有 | 已就绪 |
| CONFIG_SERIAL_8250 | 有 | 工作中 |
| CONFIG_BLK_DEV_INITRD | 有 | 工作中 |
| CONFIG_EXT4_FS | 隐式 (POSIX_ACL=y) | 需验证 |
| CONFIG_FUSE_FS | 有 | 已就绪 |
| CONFIG_OVERLAY_FS | 有 | 已就绪 |
| CONFIG_ANDROID_BINDER_IPC | 有 | 已就绪（未测试） |
| CONFIG_ANDROID_BINDERFS | 有 | 已就绪（未测试） |
| CONFIG_NAMESPACES | 有 (USER_NS=y) | 已就绪 |
| CONFIG_CGROUPS (全系列) | 有 | 已就绪 |
| CONFIG_SECURITY (AppArmor/SELinux) | 有 | 已就绪 |

**关键发现**: 内核配置非常完整，远超当前 VM 实际使用的功能。virtio-blk/net/fs/vsock、9p、binder、namespace/cgroup 全部已配置，等待 crosvm 侧设备实现。

---

## 四、virtio 设备栈状态

这是当前最大的差距。架构设计了完整的 virtio 设备集，但大部分尚未在 macOS run_config 中激活。

| 设备 | 内核 defconfig | crosvm 后端代码 | macOS run_config | 端到端可用 |
|------|---------------|----------------|-----------------|-----------|
| **virtio-blk** | CONFIG_VIRTIO_BLK=y | macOS 后端 40 行，功能完整 | 未注册 | 否 |
| **virtio-net** | CONFIG_VIRTIO_NET=y | macOS stub（空文件） | 未注册 | 否 |
| **virtio-fs** | CONFIG_VIRTIO_FS=y | FUSE server 跨平台实现存在 | 未注册 | 否 |
| **virtio-9p** | CONFIG_9P_FS=y | 完整实现，但 cfg_if 限 Linux | 未注册 | 否 |
| **virtio-vsock** | CONFIG_VIRTIO_VSOCKETS=y | macOS stub（空 struct） | 未注册 | 否 |
| **virtio-console** | N/A | macOS 输入线程实现 110 行 | 未注册 | 否 |
| **virtio-gpu** | 未配置 | 无 macOS 实现 | 未注册 | 否 |
| **串口 (8250)** | CONFIG_SERIAL_8250=y | 跨平台实现 | 已注册 | **是** |

**PCI 传输层**: 已工作。aarch64 上所有 virtio 设备通过 PCI 总线注册，FDT 包含 PCI 主桥和中断映射。

**中断路由**: 已工作。PCI 设备 → GIC SPI → vCPU。

---

## 五、宿主端（Go）状态

| 组件 | 规划 | 实际 |
|------|------|------|
| aetheria CLI | 管理 VM 生命周期 | 7 行空 main.go |
| aetheria daemon | 后台驻留，管理 crosvm 进程 | 不存在 |
| aetheria-agent | Guest 内 PID 1，vsock gRPC | 7 行空 main.go |
| proto 定义 | vsock gRPC 接口 | 目录为空 |
| internal/ 包 | agent, api, crosvm, daemon, machine, network, storage | 目录结构存在，无代码 |

**评估**: Go 侧完全未开始。当前 VM 通过直接调用 `crosvm run` 命令启动，无 daemon 管理。

---

## 六、镜像构建（aetheria-forge）

| 组件 | 规划 | 实际 |
|------|------|------|
| rootfs 构建器 | 下载发行版、构建 ext4 镜像 | 不存在 |
| 内核打包 | 打包 vmlinux + modules | 不存在（手动构建） |
| 镜像组装 | 合并 kernel + rootfs + 配置 | 不存在 |
| CI/CD | 自动构建发布 | 不存在 |

**当前 rootfs**: 手动构建的 busybox initramfs (920KB)，只包含 `/bin/busybox` + `/init` 脚本。

---

## 七、与架构设计的整体对比

### 已完成（符合设计）

1. **crosvm HVF 移植** — 设计核心，已验证可行性并实现。1,694 行 vs 预期 2,500 行。
2. **自定义内核** — mainline 6.12.15，defconfig 包含所有规划的功能（virtio 全系列、binder、namespace/cgroup、安全模块）。
3. **macOS 平台基础** — kqueue event loop、async reactor、内存映射、终端全部工作。
4. **串口控制台** — 交互式 shell，60ms 启动到 prompt。

### 部分完成（需要补全）

5. **virtio 设备栈** — PCI 传输层和中断路由工作，但 blk/net/fs/vsock 设备未在 run_config 注册。其中 blk 和 9p 的后端代码已存在，只差 wiring。
6. **rootfs** — 有 busybox initramfs 临时方案，需要 Alpine/Debian 完整系统。

### 未开始（设计中有但零代码）

7. **virtio-net 网络** — 需要 vmnet.framework 后端（已完成技术调研）。
8. **virtio-vsock** — 需要 userspace 实现。
9. **Go 宿主端** — CLI、daemon、agent 全部为空 placeholder。
10. **容器层 (nspawn/LXC)** — 完全未开始，依赖 vsock + agent。
11. **gfxstream 图形** — 完全未开始，依赖 virtio-gpu。
12. **镜像构建 (forge)** — 空仓库。
13. **多 vCPU** — 单 vCPU 架构限制（ISS-004），需要重构 build_vm 流程。
14. **Windows WHPX** — 完全未开始。

### 设计偏差

| 偏差 | 原因 | 影响 |
|------|------|------|
| GIC 需要自研 | 调研时未预见 macOS 14/15 GIC API 差异 | 增加 614 行代码，但已解决 |
| PSCI 需要 userspace 模拟 | KVM 内核处理 PSCI，HVF 不处理 | 增加 169 行 PsciDevice |
| 单 vCPU 限制 | HVF vCPU 线程亲和性约束 | 需要架构调整才能支持多核 |
| SOCK_SEQPACKET 不可用 | macOS 不支持 AF_UNIX SOCK_SEQPACKET | 用 SOCK_STREAM 替代，已解决 |
| 宿主端暂用 crosvm CLI 直接启动 | Go daemon 未实现 | 短期可接受，长期需要 daemon |

---

## 八、代码量统计

| 模块 | 新增代码行数 | 语言 |
|------|-------------|------|
| HVF hypervisor 后端 | 1,694 | Rust |
| GIC 中断控制器 | 614 | Rust |
| 平台基础库 (base) | ~800 新增 (2,565 总计含上游) | Rust |
| 异步执行器 (cros_async) | ~780 新增 (947 总计) | Rust |
| PSCI 设备 | 169 | Rust |
| run_config 主入口 | 575 | Rust |
| 串口/终端 | 329 | Rust |
| 其他 macOS 适配 | ~300 | Rust |
| 内核 defconfig | 513 | Kconfig |
| 构建脚本 | ~100 | Shell |
| 文档 (docs/) | ~1,500 | Markdown |
| **总计** | **~7,400 (Rust) + ~600 (配置/脚本)** | |

---

## 九、下一步路线图

已制定 [plan/virtio-stack.md](plan/virtio-stack.md)（当前 PAUSED）:

```
Phase 1: virtio-blk + Alpine rootfs     ← 后端代码已有，需 wiring + rootfs 构建
Phase 2: virtio-9p 文件共享             ← 实现已有，需 ungating + wiring
Phase 3: virtio-net + vmnet.framework   ← 需要新写 vmnet 后端
Phase 4: virtio-vsock                   ← 需要新写 userspace vsock
Phase 5: 集成测试
```

之后:
- Go 宿主端 (CLI + daemon + agent)
- nspawn 容器层
- 多 vCPU 支持
- virtio-gpu + gfxstream
- Windows WHPX 移植
