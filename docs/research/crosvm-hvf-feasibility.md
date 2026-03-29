# crosvm HVF 后端移植可行性分析

## 1. 背景

Aetheria 的架构选择 crosvm 作为 VMM，但 crosvm 官方 **不支持 macOS**。crosvm 的 hypervisor 后端只有：

- KVM（Linux，主力）
- WHPX + HAXM（Windows）
- Gunyah / GenieZone / Halla（Android 专用 ARM SoC）

要在 macOS 上使用 crosvm，需要自行实现 Apple Hypervisor.framework (HVF) 后端。本文档评估该移植的技术可行性和风险。

来源：[crosvm.dev/book/hypervisors.html](https://crosvm.dev/book/hypervisors.html)，[github.com/google/crosvm](https://github.com/google/crosvm) README "Supported Hypervisors" 部分。

## 2. crosvm hypervisor 后端架构

### 2.1 目录结构

```
hypervisor/src/
├── lib.rs          — trait 定义（Hypervisor, Vm, Vcpu）
├── caps.rs         — 能力枚举
├── aarch64.rs      — ARM64 架构特定类型
├── x86_64.rs       — x86_64 架构特定类型
├── kvm/            — KVM 后端（Linux）
│   ├── mod.rs        1363 行
│   ├── aarch64.rs    1014 行
│   ├── x86_64.rs     1930 行
│   └── cap.rs         135 行
├── whpx/           — WHPX 后端（Windows）
│   ├── vm.rs         1140 行
│   ├── vcpu.rs       1554 行
│   ├── types.rs       836 行
│   └── whpx_sys.rs    260 行
├── haxm/           — HAXM 后端（Windows Intel）
├── geniezone/      — GenieZone（Android Qualcomm）
├── gunyah/         — Gunyah（Android Qualcomm）
└── halla/          — Halla（Android Exynos）
    没有 hvf/ 目录
```

### 2.2 需要实现的三个核心 trait

以下从 `hypervisor/src/lib.rs` 提取：

```rust
/// 最简单 — hypervisor 实例管理
pub trait Hypervisor: Send {
    fn try_clone(&self) -> Result<Self> where Self: Sized;
    fn check_capability(&self, cap: HypervisorCap) -> bool;
}

/// 中等 — VM 生命周期和内存管理
pub trait Vm: Send {
    fn try_clone(&self) -> Result<Self> where Self: Sized;
    fn check_capability(&self, c: VmCap) -> bool;
    fn get_memory(&self) -> &GuestMemory;
    fn add_memory_region(...) -> Result<MemSlot>;
    fn remove_memory_region(...) -> Result<Box<dyn MappedRegion>>;
    fn create_device(&self, kind: DeviceKind) -> Result<SafeDescriptor>;
    fn register_ioevent(...) -> Result<()>;
    fn unregister_ioevent(...) -> Result<()>;
    fn handle_io_events(&self, addr: IoEventAddress, data: &[u8]) -> Result<()>;
    // ... 还有约 10 个方法
}

/// 最复杂 — vCPU 运行和 exit 处理
pub trait Vcpu: downcast_rs::DowncastSync {
    fn run(&mut self) -> Result<VcpuExit>;
    fn handle_mmio(&self, handle_fn: &mut dyn FnMut(IoParams) -> Result<()>) -> Result<()>;
    fn handle_io(&self, handle_fn: &mut dyn FnMut(IoParams)) -> Result<()>;
    fn id(&self) -> usize;
    fn set_immediate_exit(&self, exit: bool);
    fn on_suspend(&self) -> Result<()>;
    // ... 还有约 5 个方法
}
```

### 2.3 VcpuExit 枚举（需要映射的 exit 类型）

```rust
pub enum VcpuExit {
    Io,                    // IO port 访问（x86 专用，ARM64 不需要）
    Mmio,                  // MMIO 访问（核心）
    IoapicEoi { vector },  // x86 专用
    Exception,
    Hypercall,
    Debug,
    Hlt,
    IrqWindowOpen,
    Shutdown(...),
    Intr,
    MsrAccess,             // 系统寄存器访问
    Cpuid { entry },       // x86 专用
    // ... 其他
}
```

## 3. Apple Hypervisor.framework API

### 3.1 核心 API（ARM64 Apple Silicon）

```
VM 管理：
  hv_vm_create(config)          — 创建 VM
  hv_vm_destroy()               — 销毁 VM
  hv_vm_map(addr, ipa, size, flags) — 映射 guest 物理内存
  hv_vm_unmap(ipa, size)        — 取消映射

vCPU 管理：
  hv_vcpu_create(&vcpu, &exit_info, config) — 创建 vCPU（绑定当前 pthread）
  hv_vcpu_destroy(vcpu)         — 销毁 vCPU
  hv_vcpu_run(vcpu)             — 运行 vCPU 直到 exit
  hv_vcpu_get_reg(vcpu, reg, &val) — 读通用寄存器
  hv_vcpu_set_reg(vcpu, reg, val)  — 写通用寄存器
  hv_vcpu_get_sys_reg(vcpu, reg, &val) — 读系统寄存器
  hv_vcpu_set_sys_reg(vcpu, reg, val)  — 写系统寄存器

中断：
  hv_vcpu_set_pending_interrupt(vcpu, type, pending) — 注入中断
  hv_vcpu_get_pending_interrupt(vcpu, type, &pending) — 查询中断

定时器：
  hv_vcpu_set_vtimer_mask(vcpu, masked) — 虚拟定时器掩码
  hv_vcpu_get_vtimer_offset(vcpu, &offset) — 定时器偏移

权限要求：
  需要 com.apple.security.hypervisor entitlement
  需要 codesign 签名
```

### 3.2 HVF exit 信息结构（ARM64）

`hv_vcpu_create` 返回的 `exit_info` 指向：

```c
typedef struct {
    hv_exit_reason_t reason;      // HV_EXIT_REASON_EXCEPTION 等
    union {
        struct {
            uint64_t syndrome;        // ARM ESR_EL2 — 包含 exit 的所有信息
            uint64_t virtual_address;
            uint64_t physical_address;
        } exception;
    };
} hv_vcpu_exit_t;
```

ARM64 的 exit 信息全部编码在 `syndrome`（即 ESR_EL2 寄存器）中，通过 Exception Class (EC) 字段区分类型。

## 4. HVF exit → crosvm VcpuExit 映射

以下基于 QEMU HVF ARM64 实现（`target/arm/hvf/hvf.c`，2602 行）中 `hvf_handle_exception` 函数的分析：

```
HVF ARM64 Exception Class        crosvm VcpuExit     处理方式
─────────────────────────────────────────────────────────────────

EC_DATAABORT (0x24)             → VcpuExit::Mmio      从 syndrome 解析:
                                                        iswrite = (syndrome >> 6) & 1
                                                        sas = (syndrome >> 22) & 3  (大小: 1<<sas)
                                                        srt = (syndrome >> 16) & 0x1f (寄存器号)
                                                        ipa = exit_info.physical_address
                                                      写: val = get_reg(srt), 回调(addr, size, Write(val))
                                                      读: 回调(addr, size, Read), set_reg(srt, result)
                                                      advance PC += 4

EC_SYSTEMREGISTERTRAP (0x18)    → VcpuExit::MsrAccess  isread = (syndrome >> 0) & 1
                                                        rt = (syndrome >> 5) & 0x1f
                                                        reg = syndrome & SYSREG_MASK
                                                      读/写对应系统寄存器

EC_WFX_TRAP (0x01)              → VcpuExit::Hlt        WFI → halt vCPU 等待中断
                                                        WFE → 可忽略，advance PC

EC_AA64_HVC (0x16)              → VcpuExit::Hypercall   读 x0-x5 获取参数
                                                        处理 PSCI 调用（CPU on/off/reset）
                                                        不 advance PC

EC_AA64_SMC (0x17)              → VcpuExit::Hypercall   同上但 advance PC += 4

EC_SOFTWARESTEP (0x32/0x33)     → VcpuExit::Debug
EC_AA64_BKPT (0x3C)             → VcpuExit::Debug
EC_BREAKPOINT (0x30/0x31)       → VcpuExit::Debug
EC_WATCHPOINT (0x34/0x35)       → VcpuExit::Debug

EC_INSNABORT (0x20)             → VcpuExit::Exception  指令获取异常（通常是 bug）
```

### 为什么 ARM64 的 exit 处理比 x86 简单

| 维度 | x86 (WHPX) | ARM64 (HVF) |
|------|-----------|-------------|
| MMIO 解码 | 需要指令模拟器（x86 指令变长 1-15 字节） | syndrome 直接提供地址/大小/方向/寄存器 |
| IO port | 需要处理 IN/OUT 指令 | ARM64 没有 IO port 概念 |
| CPUID | 需要处理 CPUID exit | ARM64 没有 CPUID |
| APIC | 需要处理 APIC 相关 exit | ARM64 用 GIC，HVF 内置处理 |
| 指令推进 | 需要计算指令长度 | 固定 +4（ARM64 指令固定 32 位） |

**WHPX 的 vcpu.rs 有 1554 行，HVF 的 vcpu.rs 估计 800-1200 行足够。**

## 5. 平台适配层分析

### 5.1 crosvm 已有 macOS 骨架

crosvm 在 `base/src/sys/` 下已经为 macOS 创建了平台抽象层：

```
base/src/sys/
├── linux/      24 个文件 — 完整实现
├── windows/    31 个文件 — 完整实现
├── macos/       5 个文件 — 骨架存在
│   ├── event.rs   ✅ 已实现（基于 kqueue）
│   ├── kqueue.rs  ✅ 已实现（完整的 kevent64 封装）
│   ├── timer.rs   ✅ 已实现
│   ├── net.rs     ✅ 已实现
│   └── mod.rs     ⚠️ 33 个 todo!() 待实现
└── unix/       13 个文件 — Linux 和 macOS 共享的 POSIX 层
```

### 5.2 已实现的部分

**event.rs** — 完整的事件通知机制，基于 kqueue：
- `PlatformEvent::new()` — 创建 kqueue + 注册 EVFILT_USER
- `signal()` — 触发 NOTE_TRIGGER
- `wait()` / `wait_timeout()` — kevent 等待
- `try_clone()` — 复制

**kqueue.rs** — 完整的 BSD kqueue 封装：
- `Kqueue::new()` — 创建 kqueue fd + CLOEXEC
- `kevent()` — 支持 changelist、eventlist、timeout
- 错误处理完备

这意味着 **eventfd → kqueue 的核心适配已经完成**。

### 5.3 未实现的 33 个 todo!()

按难度分类：

**极简（直接调 POSIX/macOS API，共 ~50 行）：**
```
getpid()                → libc::getpid()
set_thread_name(name)   → pthread_setname_np(pthread_self(), name)
SafeDescriptor::eq()    → self.as_raw_descriptor() == other.as_raw_descriptor()
open_file_or_duplicate  → File::open() 或 fcntl(F_DUPFD_CLOEXEC)
```

**简单（参考 Linux/unix 实现微调，共 ~200 行）：**
```
MemoryMapping           → mmap/munmap/mprotect（与 Linux 基本相同）
SharedMemory            → shm_open + ftruncate
file_punch_hole         → fcntl(F_PUNCHHOLE) 或 fallocate 等价
file_write_zeroes_at    → pwrite 写零
ioctl 系列              → libc::ioctl（macOS 和 Linux 接口一致）
syslog                  → syslog(3) 或 os_log
```

**中等（需要设计，共 ~300 行）：**
```
EventContext            → 用 kqueue 实现 epoll 语义
  new()                 → 创建 kqueue
  add_for_event()       → kevent 注册 EVFILT_READ/WRITE
  modify()              → kevent EV_ADD 覆盖
  delete()              → kevent EV_DELETE
  wait/wait_timeout()   → kevent 等待 + 翻译为 TriggeredEvent

get_cpu_affinity        → thread_policy_get (macOS API)
set_cpu_affinity        → thread_policy_set (macOS API，有限支持)
enable_high_res_timers  → mach_timebase_info 已是高精度，可能 no-op
```

**总估计：补齐 33 个 todo!() 约 500-800 行 Rust。**

## 6. gfxstream macOS 兼容性分析

### 6.1 gfxstream 已有完整的 macOS 代码路径

从 `host/vulkan/VkCommonOperations.cpp` 源码确认（非推测，实际代码）：

**编译支持：**
```cmake
# CMakeLists.txt
if (APPLE)
    add_definitions("-DVK_USE_PLATFORM_METAL_EXT -DVK_USE_PLATFORM_MACOS_MVK")
    add_compile_definitions(VK_USE_PLATFORM_MACOS_MVK)
    add_compile_definitions(VK_USE_PLATFORM_METAL_EXT)
```

**运行时 MoltenVK 检测和适配：**
```cpp
#ifdef __APPLE__
#include <CoreFoundation/CoreFoundation.h>
#include <vulkan/vulkan_beta.h>  // MoltenVK portability extensions

// 外部内存使用 Metal Heap（非 fd/Win32 handle）
if (mInstanceSupportsMoltenVK) {
    return VK_EXTERNAL_MEMORY_HANDLE_TYPE_MTLHEAP_BIT_EXT;
}

// MoltenVK 专用扩展
std::vector<const char*> moltenVkInstanceExtNames = {
    VK_MVK_MACOS_SURFACE_EXTENSION_NAME,
    VK_KHR_PORTABILITY_ENUMERATION_EXTENSION_NAME,
};
std::vector<const char*> moltenVkDeviceExtNames = {
    VK_KHR_PORTABILITY_SUBSET_EXTENSION_NAME,
    VK_EXT_METAL_OBJECTS_EXTENSION_NAME,
    VK_EXT_EXTERNAL_MEMORY_METAL_EXTENSION_NAME,
};

// 启用 Vulkan portability 模式
if (useMoltenVK) {
    instCi.flags |= VK_INSTANCE_CREATE_ENUMERATE_PORTABILITY_BIT_KHR;
}
#endif
```

**产出物：** `libgfxstream_backend.dylib`（macOS 动态库，CMake 构建目标）

### 6.2 MoltenVK 对 gfxstream 需求的覆盖

| gfxstream 需要的扩展 | MoltenVK 支持 | 说明 |
|---------------------|-------------|------|
| `VK_MVK_macos_surface` | ✅ | MoltenVK 自带 |
| `VK_KHR_portability_enumeration` | ✅ | MoltenVK 自带 |
| `VK_KHR_portability_subset` | ✅ | MoltenVK 自带 |
| `VK_EXT_metal_objects` | ✅ | MoltenVK 自带 |
| `VK_EXT_external_memory_metal` | ✅ | MoltenVK 自带 |
| `VK_KHR_external_memory_capabilities` | ✅ | Vulkan 1.1 core |
| `VK_KHR_external_semaphore_capabilities` | ✅ | Vulkan 1.1 core |
| `VK_KHR_get_physical_device_properties_2` | ✅ | Vulkan 1.1 core |

MoltenVK 截至 2026 年 2 月提供接近完整的 Vulkan 1.4 支持（来源：Grokipedia MoltenVK 词条）。gfxstream 使用的都是基础 Vulkan 功能和 MoltenVK 自带的平台扩展，不涉及 MoltenVK 的已知弱项（sparse binding 等）。

### 6.3 gfxstream 与 crosvm 的集成

crosvm 中 gfxstream 的集成位于 `devices/src/virtio/gpu/`，是 virtio-gpu 设备的一个渲染后端。这部分代码：

- 是 **平台无关的 Rust 代码**
- 只通过 C FFI 调用 `libgfxstream_backend` 的 API
- 不直接调用任何平台图形 API

只要 `libgfxstream_backend.dylib` 在 macOS 上正确编译和链接（已通过 CMake 配置确认支持），crosvm 的 virtio-gpu + gfxstream 设备代码 **不需要修改**。

### 6.4 剩余不确定性

- gfxstream + MoltenVK 的实际渲染质量和性能未经 Aetheria 团队实测
- MoltenVK 在 macOS Tahoe (26.x) 上有已知的 crash issue（[MoltenVK #2700](https://github.com/KhronosGroup/MoltenVK/issues/2700)），需要关注修复进度
- gfxstream 的 macOS 代码路径主要由 Android Emulator 团队维护，可能不如 Linux/Windows 路径经过充分测试

## 7. 沙盒禁用影响分析

### 7.1 crosvm 的沙盒机制

crosvm 在 Linux 上使用 minijail（基于 Linux namespace + seccomp-bpf）为每个 virtio 设备进程创建沙盒。这些机制是 Linux 专有的：

| 机制 | Linux | macOS |
|------|-------|-------|
| seccomp-bpf | ✅ | ❌ 不存在 |
| Linux namespace | ✅ | ❌ 不存在 |
| Linux capabilities | ✅ | ❌ 不存在 |
| pivot_root | ✅ | ❌ 不存在 |

### 7.2 先例：crosvm Windows 后端

crosvm 的 Windows 后端同样面临无 minijail 的问题，解决方式是 **禁用沙盒**。设备不在独立进程中运行，而是在 crosvm 主进程内作为线程运行。macOS 可以采用相同策略。

### 7.3 禁用沙盒的安全影响

**不影响 VM 隔离：** guest 与 host 之间的隔离由 CPU 硬件虚拟化提供（HVF），与沙盒机制无关。

**不影响容器隔离：** VM 内的 nspawn 容器隔离由 Linux namespace/cgroup 提供，运行在 guest 内核中，与 host 侧沙盒无关。

**影响 crosvm 内部设备隔离：** 如果 crosvm 的某个 virtio 设备代码（如 virtio-gpu / gfxstream）存在漏洞，攻击者可以通过构造恶意 virtio 命令触发漏洞，获得 crosvm 进程的权限。有沙盒时攻击者被困在设备进程的沙盒里；无沙盒时攻击者获得整个 crosvm 进程权限。

**实际风险评估：**
- 个人开发者使用（运行自己的容器）：无实际风险
- 运行不可信 APK：有理论风险，但需要同时满足 (a) APK 能构造特定 virtio 命令 + (b) 对应设备代码有匹配的漏洞，概率极低
- 多租户云部署：风险较高，需要后续补上安全隔离

### 7.4 未来可选的 macOS 安全增强

- macOS App Sandbox（需要作为 App 分发）
- 以低权限用户运行 crosvm 进程
- 使用 macOS sandbox-exec（Seatbelt，非公开但广泛使用）
- 将不同 virtio 设备放入不同进程，用 POSIX 权限限制

## 8. 工作量估算

### 8.1 分阶段计划

**Phase 1：HVF 后端 MVP — 能 boot Linux（4-6 周）**

| 模块 | 估计代码量 | 说明 |
|------|----------|------|
| `hypervisor/src/hvf/mod.rs` | ~300 行 | Hypervisor + Vm trait 实现，HVF FFI 绑定 |
| `hypervisor/src/hvf/vcpu.rs` | ~800-1200 行 | Vcpu trait，run loop，exit 翻译，MMIO 处理 |
| `hypervisor/src/hvf/types.rs` | ~200 行 | HVF 类型定义和转换 |
| `hypervisor/src/hvf/ffi.rs` | ~300 行 | Apple HVF C API 的 Rust unsafe 绑定 |
| 合计 | ~1600-2000 行 | |

参考：WHPX 后端 ~3800 行（含 x86 复杂度），HVF ARM64 应更简单。

**Phase 2：平台适配层补齐（2-4 周）**

| 模块 | 估计代码量 | 说明 |
|------|----------|------|
| `base/src/sys/macos/mod.rs` 补齐 | ~500-800 行 | 33 个 todo!() 实现 |
| EventContext (kqueue) | ~200 行（含在上面） | 最复杂的一个 |
| MemoryMapping (mmap) | ~150 行（含在上面） | 参考 Linux 实现 |
| 合计 | ~500-800 行 | |

**Phase 3：gfxstream + GPU（4-8 周）**

| 任务 | 说明 |
|------|------|
| 编译 libgfxstream_backend.dylib | CMake 已支持 macOS，需要验证 |
| 链接 crosvm virtio-gpu + gfxstream | 设备代码平台无关，需要链接配置 |
| MoltenVK 集成和测试 | 安装 MoltenVK，配置 ICD loader |
| 渲染验证 | 启动 Android guest，验证图形输出 |

**Phase 4：稳定化和优化（持续）**

| 任务 | 说明 |
|------|------|
| 定时器和中断处理完善 | vtimer 掩码、GIC 交互 |
| 多 vCPU 稳定性 | pthread 绑定、vCPU 同步 |
| 内存热插拔 | balloon 设备支持 |
| 性能调优 | exit 频率优化、内存映射策略 |

### 8.2 总工作量

| 阶段 | 时间 | 产出 |
|------|------|------|
| Phase 1 | 4-6 周 | 能 boot Linux，串口输出 |
| Phase 2 | 2-4 周 | crosvm 在 macOS 上完整运行 |
| Phase 3 | 4-8 周 | GPU 加速图形工作 |
| Phase 4 | 持续 | 生产质量 |

**总计：一个工程师全职约 3-5 个月达到可用状态。**

### 8.3 代码量对比

| 后端 | 代码行数 | 备注 |
|------|---------|------|
| KVM (Linux) | ~4400 行 | 最成熟，含 x86 + ARM64 |
| WHPX (Windows) | ~3800 行 | 仅 x86 |
| **HVF (macOS) 估计** | **~2000-2800 行** | 仅 ARM64，exit 处理更简单 |
| QEMU HVF (参考) | ~3200 行 C | 含通用加速器框架 |

## 9. 上游合入可能性

crosvm-dev 邮件列表中有人询问过 macOS 移植是否会被接受（[讨论链接](https://groups.google.com/a/chromium.org/g/crosvm-dev/c/0n3dPJAl6tQ)），但讨论内容无法公开访问，无法确认 Google 的态度。

有利因素：
- crosvm 已经有 macOS 平台抽象层骨架（说明有人在 Google 内部探索过）
- crosvm 接受了非 Linux 后端（WHPX、HAXM）
- crosvm 的 hypervisor trait 架构设计就是为了支持多后端

不利因素：
- Google 可能认为 macOS 不是 Chrome OS/Android 的目标平台
- HVF 后端的 CI 测试需要 macOS runner

**即使不被上游接受，维护一个 fork 的成本很低** — 只是 `hypervisor/src/hvf/` 目录下的 ~2500 行代码，与 crosvm 主体代码耦合度低。

## 10. 最终风险矩阵

| 风险项 | 评级 | 理由 | 缓解措施 |
|--------|------|------|---------|
| VM Exit 翻译 | **中** | ARM64 比 x86 简单；syndrome 直接提供 MMIO 参数；QEMU 有完整参考实现（2602 行 C） | 参考 QEMU hvf.c 的 hvf_handle_exception 函数 |
| 平台适配 (todo!()) | **低** | crosvm 已有 macOS 骨架（5 文件 + kqueue 完成）；33 个 todo!() 多数是标准 POSIX 映射 | 逐个补齐，参考 Linux/unix 实现 |
| gfxstream macOS | **低** | gfxstream 已有完整 `#ifdef __APPLE__` 代码路径；CMake 支持 macOS；MoltenVK 扩展全部覆盖 | 先验证编译，再验证渲染 |
| 沙盒禁用 | **低（可接受）** | Windows 后端先例；不影响 VM/容器隔离；个人开发者场景无实际风险 | 先禁用，未来用 macOS 进程级隔离补上 |
| MoltenVK 稳定性 | **低-中** | macOS Tahoe 上有已知 crash（#2700）；整体 Vulkan 1.4 覆盖度好 | 跟进 MoltenVK 修复；可降级 macOS 版本 |
| 上游合入 | **不确定** | 无法确认 Google 态度 | 可维护 fork，耦合度低 |

## 11. 结论

**crosvm HVF 后端移植技术上完全可行，没有发现阻断性问题。**

核心依据：
1. crosvm 已有 macOS 平台抽象层骨架（event/kqueue/timer 已实现）
2. ARM64 的 HVF exit 处理比 x86 WHPX 更简单（无需指令模拟器）
3. gfxstream 已有完整的 macOS/MoltenVK 代码路径（非待实现功能）
4. QEMU HVF ARM64 后端提供了完整的参考实现
5. 估计 ~2500 行 Rust 代码，3-5 个月工程投入

最大的不确定性不是技术，而是上游合入策略。但即使维护独立 fork，成本也可控。
