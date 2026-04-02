# Filesystem Sharing: Deep Research for Aetheria

## Executive Summary

Aetheria needs macOS host-to-Linux guest filesystem sharing using crosvm with HVF. After researching every major container/VM runtime on macOS, the optimal approach is a **phased strategy**: start with **virtio-9p** (already ported to macOS in aetheria-crosvm), then implement **virtio-fs with an embedded passthrough server** (the libkrun model), and later add **DAX for mmap-heavy workloads**.

---

## 1. OrbStack

### Mechanism
- **Protocol**: VirtioFS (FUSE over virtio), with custom dynamic caching layer
- **VMM**: Apple Virtualization.framework (not HVF directly)
- **Architecture**: Single lightweight Linux VM, VZ-provided virtiofs, with OrbStack's own caching/optimization layer on top

### How It Works
OrbStack uses Apple's VZ framework, which provides `VZVirtioFileSystemDeviceConfiguration` -- Apple's built-in virtiofs implementation. OrbStack then adds a proprietary optimization layer:
1. Apple's VZ virtiofs server handles FUSE requests via shared memory
2. OrbStack adds "dynamic caching" that aggressively caches metadata and data
3. Per-call overhead reduced by up to 10x through their custom stack
4. Bidirectional sharing via `~/OrbStack` mount point

### Performance
| Benchmark | OrbStack | Native macOS | % of Native |
|-----------|----------|-------------|-------------|
| pnpm install | 12.2s | 10.9s | 88% |
| yarn install | 9.8s | 7.9s | 77% |
| rm -rf node_modules | 4.0s | 3.6s | 87% |
| Postgres pgbench | 8,998 TPS | 11,785 TPS | 76% |

Overall: **75-95% of native macOS performance**, 2-10x faster than competing solutions.

### Why This Approach
- VZ framework provides virtiofs "for free" -- no need to implement a FUSE server
- Apple's implementation is kernel-optimized (runs in a private XPC service)
- OrbStack's value-add is the caching/optimization layer, not the transport
- **Not replicable by Aetheria**: requires VZVirtualMachine, which we don't use

### Key Insight
OrbStack's filesystem performance advantage comes from two layers: (1) Apple's efficient VZ virtiofs kernel implementation, and (2) OrbStack's proprietary caching. We cannot use layer (1) since we use crosvm/HVF, not VZ.

---

## 2. Docker Desktop for Mac

### Mechanism History
1. **osxfs** (2016-2020): Custom FUSE-based file server, very slow
2. **gRPC-FUSE** (2020-2022): gRPC-based FUSE server, moderate performance
3. **virtiofs** (2022-present): Apple VZ framework virtiofs, current default
4. **Docker VMM** (2024-present): Custom hypervisor with optimized virtiofs

### Current Implementation (2025-2026)
- **Apple VZ mode**: Uses `VZVirtioFileSystemDeviceConfiguration` (same as OrbStack)
- **Docker VMM mode**: Custom hypervisor (closed-source) with virtiofs, claims 2x faster cold cache and up to 25x faster warm cache vs Apple VZ
- **Synchronized file shares**: Mutagen-based bidirectional sync to ext4 volumes inside VM, 2-10x faster than virtiofs bind mounts (paid feature)

### Performance (2025 benchmarks, bind mount test)
| Configuration | Time | Notes |
|--------------|------|-------|
| Docker-VZ | 9.53s | Standard Apple VZ virtiofs |
| Docker-VMM | 8.47s | Custom hypervisor, beta |
| Docker-VZ + sync | 3.88s | Mutagen sync, paid feature |
| OrbStack | 4.22s | Comparison point |
| Native Linux Docker | 5.29s | Baseline |
| Lima (VZ) | 8.99s | Open source baseline |

### Architecture Detail
VirtIO drivers implemented by Apple on the host communicate with the virtiofs driver in the guest kernel through VirtIO queues. Docker's VZ mode adds attribute caching timeout extensions and host change notification optimizations.

### Key Insight
Docker's progression (osxfs -> gRPC-FUSE -> virtiofs -> Docker VMM) shows the industry converging on virtiofs. Their "synchronized file shares" (Mutagen) is a pragmatic workaround for virtiofs overhead -- copy files to native ext4, keep them synced. This is architecturally similar to what we might consider.

---

## 3. Lima (Colima / Rancher Desktop)

### Supported Mount Types

| Mount Type | Transport | Default For | Requirements |
|-----------|-----------|-------------|-------------|
| reverse-sshfs | SFTP over SSH | Lima < 1.0 | Any VM type |
| 9p (virtfs) | virtio-9p-pci | Lima 1.0+ (QEMU) | QEMU only |
| virtiofs | VZ framework | Lima 1.0+ (VZ) | macOS 13+, VZ VM type |
| wsl2 | 9P (internal) | Windows | Windows only |

### 9P Details
- Uses QEMU's `virtio-9p-pci` device (NOT virtio-fs)
- Protocol: 9p2000.L (default), configurable
- Security model: "none" (default), passthrough, mapped-xattr, mapped-file
- msize: 128 KiB default (packet payload size)
- Cache: configurable (none, loose, fscache, mmap)
- **Performance**: Poor. Users report `git status` taking 30+ seconds on medium projects
- **Compatibility**: Does not work on CentOS/Rocky/AlmaLinux (kernel lacks CONFIG_NET_9P_VIRTIO)

### virtiofs Details
- Only available with VZ VM type (not QEMU on macOS)
- Uses Apple's `VZVirtioFileSystemDeviceConfiguration`
- macOS 13+ required
- Better than 9P but still slower than native

### reverse-sshfs Details
- Host runs SFTP server, SSH tunnels to guest
- No TCP ports exposed
- Moderate performance with caching enabled
- Legacy option, not recommended for new deployments

### Key Insight
Lima's experience validates that 9P is functional but slow, and virtiofs via VZ is the performance option. For QEMU-based VMs (analogous to our crosvm situation), 9P is the only realistic option Lima offers.

---

## 4. Podman Desktop / podman machine

### Mechanism
- **Default (macOS 13+)**: virtiofs via Apple VZ framework (`applehv` provider)
- **Alternative**: libkrun provider with built-in virtio-fs
- **Legacy**: 9P (Podman < 5.0)

### Podman 5.0 Transition
Podman 5.0 switched from 9P to virtiofs on macOS, significantly improving performance. Uses Apple's VZ framework for the `applehv` provider.

### libkrun Provider
When using the `libkrun` provider (krunkit), Podman uses libkrun's embedded virtio-fs passthrough server running on HVF. This is the most architecturally relevant reference for Aetheria.

### Performance
Bind mounts ~3x slower than native (improved from 5-6x). Named volumes (inside VM disk) are near-native speed.

### Key Insight
Podman's libkrun provider proves that virtio-fs CAN work on macOS with HVF, without Apple's VZ framework. libkrun embeds the passthrough server directly in the VMM process.

---

## 5. Apple Virtualization.framework virtiofs

### Implementation
- **API**: `VZVirtioFileSystemDeviceConfiguration` with `VZSharedDirectory` or `VZMultipleDirectoryShare`
- **Available since**: macOS 13 (Ventura)
- **Architecture**: VZ internally runs a virtiofs server in a sandboxed XPC service (`com.apple.Virtualization.VirtualMachine`)

### How It Works Internally
1. Host app configures `VZVirtioFileSystemDeviceConfiguration` with a tag and share
2. VZ's XPC service runs a private virtiofs/FUSE server
3. Guest mounts using `mount -t virtiofs <tag> /mnt`
4. FUSE requests travel over virtio transport (shared memory) to host XPC service
5. XPC service performs actual file I/O on macOS filesystem

### Can We Extract Just the File Sharing?
**No.** VZ's virtiofs is tightly integrated with `VZVirtualMachine`. There is no public API to use VZ's virtiofs server independently. The XPC service is private and cannot be accessed outside VZ.

### Performance Characteristics
- Significantly better than 9P or network-based approaches
- Apple's implementation benefits from kernel-level optimizations in the XPC service
- No DAX support exposed (Apple's implementation details are opaque)

### Key Insight
Apple's virtiofs is a black box -- efficient but non-extractable. Aetheria cannot use it with crosvm/HVF.

---

## 6. libkrun / krunkit

### Mechanism
- **Protocol**: virtio-fs with embedded passthrough server
- **VMM**: Custom lightweight VMM using HVF on macOS (Apple Silicon)
- **Transport**: virtio-mmio (not PCI)
- **FUSE backend**: `fuse-backend-rs` crate (from Cloud Hypervisor project)

### Architecture (Most Relevant to Aetheria)
```
macOS Host
  krunkit process
    libkrun VMM (Rust)
      HVF hypervisor backend
      virtio-mmio transport
      virtio-fs device
        fuse-backend-rs passthrough server  <-- runs IN the VMM process
        handles FUSE requests from guest
        performs host filesystem I/O

  === VM boundary (HVF) ===

  Linux Guest
    virtio-fs kernel driver
    mount -t virtiofs TAG /mnt
```

### How It Works Without virtiofsd
libkrun does NOT use an external virtiofsd daemon. Instead:
1. The `fuse-backend-rs` crate provides a Rust passthrough filesystem implementation
2. This runs as part of the VMM process itself
3. Guest FUSE requests arrive via virtio-mmio transport
4. The embedded passthrough server handles them using standard POSIX file I/O
5. No vhost-user protocol needed -- everything is in-process

### Configuration
```
krunkit --device virtio-fs,sharedDir=/path/to/share,mountTag=TAG
```
Guest mounts: `mount -t virtiofs TAG /mnt`

### macOS Compatibility
- Works on macOS with HVF (Apple Silicon)
- Requires case-sensitive APFS volume for container images
- Uses `/dev/fd/N` instead of `/proc/self/fd/N` for macOS
- No `O_PATH`, uses `O_RDONLY` equivalent
- No `renameat2`, falls back to rename+unlink

### Key Insight
**libkrun is the closest architectural model for Aetheria's filesystem sharing.** It proves that virtio-fs with an embedded passthrough server works on macOS/HVF. The `fuse-backend-rs` crate is the key component.

---

## 7. Technical Deep-Dive: Implementing virtiofs on macOS

### Option A: Port virtiofsd (External Daemon)
**Verdict: Not viable for macOS.**

The Rust virtiofsd (gitlab.com/virtio-fs/virtiofsd) has deep Linux dependencies:
- `sys/fsuid.h` (Linux-only)
- `libc::SYS_setresuid/setresgid` (Linux-only)
- `libc::SYS_renameat2` (Linux-only, macOS has no atomic rename-exchange)
- `/proc/self/fd/N` (Linux procfs)
- `O_PATH` flag (Linux-only)
- Minijail/seccomp sandboxing (Linux-only)
- Namespace isolation (Linux-only)

Would require months of porting work with questionable benefit.

### Option B: crosvm's Built-in virtio-fs (Port passthrough.rs)
**Verdict: Substantial work, but possible.**

Current state in aetheria-crosvm:
- `fuse` crate: **Linux/Android only** (`#![cfg(any(target_os = "android", target_os = "linux"))]` at line 7)
- `fs` module: Only compiled for Linux/Android (gated in `devices/src/virtio/mod.rs` line 117)
- `passthrough.rs`: Uses Linux-specific syscalls:
  - `libc::SYS_setresuid/setresgid` (credential switching)
  - `libc::fstatat64` (macOS has `fstatat` but no `fstatat64`)
  - `libc::SYS_renameat2` (no macOS equivalent)
  - `libc::SYS_copy_file_range` (no macOS equivalent)
  - Minijail sandboxing for process isolation

However, the macOS branch already has:
- `p9` module ported to macOS (with full `#[cfg(target_os = "macos")]` handling)
- vhost-user-fs backend has `sys/macos.rs` stub file

Porting would require:
1. Port `fuse` crate to macOS (remove Linux-only gating, add macOS compat)
2. Port `passthrough.rs` to macOS (replace ~15 Linux-specific syscalls)
3. Wire up the fs device in `mod.rs` for macOS target
4. Fill in the vhost-user-fs macOS backend

Estimated effort: 2-4 weeks of focused work.

### Option C: Use fuse-backend-rs (libkrun Model)
**Verdict: Most pragmatic for new implementation.**

The `fuse-backend-rs` crate from Cloud Hypervisor:
- Provides passthrough filesystem implementation
- Supports virtiofs transport layer
- Has an optional `core-foundation-sys` dependency (macOS-aware)
- Used by libkrun, which runs on macOS/HVF successfully
- More actively maintained and tested on macOS than crosvm's fuse crate

Integration approach:
1. Add `fuse-backend-rs` as a dependency
2. Create a new virtio-fs device using fuse-backend-rs instead of crosvm's fuse crate
3. Wire it into crosvm's device model
4. Use MMIO or PCI transport

Estimated effort: 3-5 weeks (more initial work than Option B, but cleaner result).

### Option D: macFUSE / FUSE-T
**Verdict: Not directly relevant, but informative.**

- **macFUSE**: Kernel extension (kext) for FUSE on macOS. Requires SIP modification, becoming harder to use with each macOS release. Not suitable for production.
- **FUSE-T**: Kext-less FUSE using NFSv4 local server. When a FUSE filesystem mounts, FUSE-T launches a local NFS server, then uses macOS's native `mount_nfs` to mount it. Better performance than macFUSE due to optimized macOS NFSv4 client. Drop-in replacement for macFUSE.

Neither is directly useful for VM filesystem sharing, but FUSE-T's NFSv4 approach is an interesting architecture reference.

### Option E: vhost-user Protocol with External Server
**Verdict: Over-engineered for our use case.**

The vhost-user protocol allows an external daemon to handle virtio device requests. In theory:
1. Write a macOS-native virtiofs server (Swift/Rust)
2. Connect to crosvm via vhost-user Unix socket
3. crosvm forwards guest FUSE requests to the external server

This adds IPC overhead and process management complexity. libkrun proves that embedding the server in the VMM is simpler and faster.

---

## 8. NFS as Alternative

### How It Would Work
1. Run NFS server on macOS host (built-in `nfsd` or custom)
2. Guest mounts NFS share over virtio-net
3. Standard NFSv4 protocol

### Advantages
- No virtio-fs implementation needed
- macOS has excellent built-in NFS server
- Standard, well-tested protocol
- Works with any VM/network configuration

### Disadvantages
- **Network overhead**: TCP/IP stack in both host and guest, even for localhost
- **Latency**: Each file operation requires network round-trip
- **Lock contention**: NFS lock/unlock protocol adds overhead
- **No mmap**: NFS does not support direct mmap of host files
- **Security**: Requires network configuration, port management
- **Small file performance**: Very poor due to per-operation latency
- macOS NFS has known performance issues (excessive lock/unlock RPCs)

### Performance
- Users report NFS on macOS being "extremely slow" for traversing directories
- Typical overhead: 5-20x slower than native for metadata-heavy operations
- Better for large sequential reads/writes but still has TCP overhead

### Verdict
NFS is a fallback option only. It would be adequate for occasional file access but completely inadequate for developer workflows (builds, git, package managers).

---

## 9. Comparison Matrix

| Runtime | Protocol | VMM | Performance | mmap | macOS Native | Open Source |
|---------|----------|-----|-------------|------|-------------|-------------|
| OrbStack | virtiofs (VZ) + custom cache | VZ | 75-95% native | Via VZ | Yes | No |
| Docker Desktop | virtiofs (VZ) | VZ / Docker VMM | 30-50% native (VZ), better with VMM | Via VZ | Yes | No |
| Lima (VZ) | virtiofs (VZ) | VZ | ~50% native | Via VZ | Yes | Yes |
| Lima (QEMU) | 9P | QEMU | ~10-20% native | Limited | Yes | Yes |
| Podman (applehv) | virtiofs (VZ) | VZ | ~33% native | Via VZ | Yes | Yes |
| Podman (libkrun) | virtiofs (embedded) | libkrun/HVF | ~30% native | No DAX | Yes | Yes |
| libkrun/krunkit | virtiofs (embedded) | libkrun/HVF | ~30% native | No DAX | Yes | Yes |
| Apple Container | Block device (ext4) | VZ | ~100% native | N/A | Yes | Yes |
| **Aetheria (target)** | **virtiofs (embedded)** | **crosvm/HVF** | **60-80% native** | **DAX later** | **Yes** | **Yes** |

---

## 10. Recommendation for Aetheria

### Phase 1: virtio-9p (Immediate, already ported)

**Use the already-ported 9P implementation as the initial filesystem sharing mechanism.**

The p9 module in aetheria-crosvm already has full macOS support:
- `p9/src/server/mod.rs`: Extensive `#[cfg(target_os = "macos")]` handling
- `p9/src/server/read_dir.rs`: macOS-specific `readdir`/`telldir`
- `devices/src/virtio/mod.rs` line 138-140: macOS target compiles p9 device

This gives us working filesystem sharing on Day 1, allowing development of the rest of the system (kernel, networking, graphics) without being blocked on filesystem.

**Expected performance**: ~10-30% of native (adequate for development, not production).

### Phase 2: virtio-fs with Embedded Passthrough (2-4 months)

**Port crosvm's passthrough.rs to macOS, extending the fuse crate.**

Rationale for porting crosvm's own implementation rather than integrating fuse-backend-rs:
1. Already intimately familiar with crosvm's codebase
2. The p9 port demonstrates the macOS compat pattern (fstatat64 -> fstat, etc.)
3. Avoids adding a large external dependency and integration layer
4. The vhost-user-fs backend already has a macOS stub (`sys/macos.rs`)

Steps:
1. Remove the `#![cfg(any(target_os = "android", target_os = "linux"))]` gate from `fuse/src/lib.rs`
2. Add `#[cfg(target_os = "macos")]` alternatives for Linux-specific code in fuse crate
3. Port `passthrough.rs`:
   - `fstatat64` -> `fstatat` (macOS uses 64-bit by default)
   - `SYS_setresuid/setresgid` -> Thread-local credential management or skip (run unsandboxed initially)
   - `SYS_renameat2` -> `renameat` (lose RENAME_NOREPLACE atomicity)
   - `SYS_copy_file_range` -> `sendfile` or `fcopyfile` on macOS
   - `/proc/self/fd/N` -> `/dev/fd/N`
   - `O_PATH` -> `O_RDONLY` with `O_NOFOLLOW`
   - Remove Minijail sandboxing (use macOS sandbox-exec or App Sandbox later)
4. Enable `pub mod fs;` in `mod.rs` for macOS target
5. Wire up `--shared-dir` CLI flag for macOS

**Expected performance**: ~40-60% of native with caching tuned.

### Phase 3: DAX Window for mmap (6+ months)

**Add DAX/shared memory support for direct file mapping.**

This is critical for developer tools that rely on mmap (gcc, cargo, ld, etc.):
1. Implement the virtiofs DAX extension in the crosvm virtio-fs device
2. Expose a PCI BAR as the DAX window (shared memory region)
3. Map host files into the DAX window on demand
4. Guest kernel uses DAX to directly access file contents without copying

This provides near-native performance for mmap-heavy workloads.

**Expected performance with DAX**: 70-90% of native.

### Phase 4: Optimizations (Ongoing)

- **Aggressive attribute caching** (like OrbStack's dynamic cache)
- **Read-ahead and write-back caching** for sequential I/O
- **Inotify bridging** for file change notifications
- **Case-fold support** for case-insensitive host filesystem

### Why NOT These Alternatives

| Alternative | Why Not |
|------------|---------|
| Apple VZ virtiofs | Requires VZVirtualMachine, incompatible with crosvm/HVF |
| External virtiofsd | Linux-only, massive porting effort, adds process management |
| NFS | Too slow for developer workflows, no mmap |
| fuse-backend-rs | Could work, but adds external dependency; crosvm's own fuse is closer to what we need |
| Mutagen/rsync sync | Eventual consistency unacceptable for builds; only useful as supplementary layer |
| FUSE-T/macFUSE | For host-side FUSE filesystems, not VM file sharing |

---

## Sources

- [OrbStack Fast Filesystem Blog](https://orbstack.dev/blog/fast-filesystem)
- [OrbStack Architecture Docs](https://docs.orbstack.dev/architecture)
- [Docker Speed Boost Blog](https://www.docker.com/blog/speed-boost-achievement-unlocked-on-docker-desktop-4-6-for-mac/)
- [Docker VMM Documentation](https://docs.docker.com/desktop/features/vmm/)
- [Docker macOS Performance 2025](https://www.paolomainardi.com/posts/docker-performance-macos-2025/)
- [Lima Filesystem Mounts](https://lima-vm.io/docs/config/mount/)
- [Lima v1.0 Mount Driver Change](https://github.com/lima-vm/lima/issues/971)
- [Podman macOS Tuning](https://ozkanpakdil.github.io/posts/my_collections/2026/2026-03-08-tuning-podman-macos-performance/)
- [Apple VZVirtioFileSystemDeviceConfiguration](https://developer.apple.com/documentation/virtualization/vzvirtiofilesystemdeviceconfiguration)
- [virtio-fs Design Document](https://virtio-fs.gitlab.io/design.html)
- [virtiofsd Rust Project](https://gitlab.com/virtio-fs/virtiofsd)
- [crosvm Filesystem Documentation](https://crosvm.dev/book/devices/fs.html)
- [crosvm passthrough.rs Source](https://crosvm.dev/doc/devices/virtio/fs/passthrough/struct.Config.html)
- [libkrun Architecture (DeepWiki)](https://deepwiki.com/containers/libkrun/3-architecture-overview)
- [libkrun GitHub](https://github.com/containers/libkrun)
- [krunkit Usage](https://github.com/containers/krunkit/blob/main/docs/usage.md)
- [fuse-backend-rs](https://github.com/cloud-hypervisor/fuse-backend-rs)
- [Running Linux microVMs on M1](https://slp.prose.sh/running-microvms-on-m1)
- [FUSE-T](https://www.fuse-t.org/)
- [virtiofs vs 9P Benchmarks](https://www.mail-archive.com/virtio-fs@redhat.com/msg02371.html)
- [virtiofs DAX Patches](https://lwn.net/Articles/813807/)
- [Jeff Geerling VirtioFS Benchmarks](https://www.jeffgeerling.com/blog/2022/new-docker-mac-virtiofs-file-sync-4x-faster)
- [Linaro Rust Device Backends](https://www.linaro.org/blog/rust-device-backends-for-every-hypervisor/)
