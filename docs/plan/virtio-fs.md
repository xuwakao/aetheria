# Plan: Host filesystem sharing on macOS

Status: COMPLETED
Created: 2026-04-01
Source: Architecture requirement — host directory sharing for container workflows

## Objective

Enable sharing macOS host directories with the guest VM, so containers can access
host files (e.g., `/Users/malihom/projects` mounted at `/mnt/host` inside VM).

## Alternatives Analysis

| Approach | Effort | Pros | Cons | Decision |
|----------|--------|------|------|----------|
| A: Native virtio-fs port | 3-6 months | Full feature parity | FUSE crate Linux-only, getdents64, namespaces, minijail | Rejected — disproportionate effort |
| B: virtio-9p (existing) | 1-2 days | `p9` crate is `cfg(unix)`, device code exists in `p9.rs`, kernel has CONFIG_9P_FS=y | Lower perf than virtio-fs for large I/O | **SELECTED** |
| C: virtiofsd external daemon | 4-8 weeks | Full virtio-fs protocol | Extra process, complex setup | Rejected — unnecessary complexity |
| D: NFS passthrough | 2-3 weeks | Mature protocol | Separate NFS server needed | Rejected — heavy for local sharing |

**Rationale for B**: The `p9` crate (v0.3.2) is `#![cfg(unix)]` with no Linux-specific syscalls (no getdents64, no namespaces). The virtio-9p device (`devices/src/virtio/p9.rs`, 220 LOC) is fully portable. The guest kernel already has `CONFIG_9P_FS=y` + `CONFIG_NET_9P_VIRTIO=y` compiled in. OrbStack and Lima both use 9P for macOS→VM file sharing. This is the minimum-effort, production-viable path.

## Phase 1: Register virtio-9p device on macOS

**Objective**: Guest can mount a host directory via `mount -t 9p`.

**Expected Results**:
1. P9 device registered in `src/crosvm/sys/macos.rs` with a hardcoded shared directory
2. PCI device `[1af4:1009]` (virtio-9p) appears in guest boot log
3. Guest can execute `mount -t 9p -o trans=virtio,version=9p2000.L host_share /mnt`
4. Files from host directory are visible inside guest at `/mnt`
5. Read and write operations work bidirectionally
6. Compiles: `cargo build --no-default-features --features net --release`

**Dependencies**: None (kernel already has 9P support)

**Risks**:
- p9 crate may have macOS compilation issues despite `cfg(unix)`
- 9P mount options may need tuning for macOS backend

## Phase 2: End-to-end test

**Objective**: Verified file sharing with read/write/mkdir/symlink.

**Expected Results**:
1. Host creates file → visible in guest mount
2. Guest creates file → visible on host
3. Large file transfer works (>1MB)
4. Directory listing correct
5. No resource leaks on unmount

**Dependencies**: Phase 1
