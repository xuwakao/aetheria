# Plan: virtiofs on macOS (full implementation)

Status: COMPLETED
Created: 2026-04-01
Source: Architecture — production-grade filesystem sharing

## Objective

Implement complete virtiofs on macOS with all performance optimizations:
virtiofs passthrough + DAX shared memory + cache=always + FSEvents notification.
Target: 70-85% native performance across all workloads.

## Architecture

```
macOS Host (crosvm)
┌─────────────────────────────────────────────────┐
│ virtio-fs device                                 │
│ ┌─────────────────┐  ┌───────────────────────┐  │
│ │ FUSE Server      │  │ DAX Window (PCI BAR)  │  │
│ │ (protocol parse) │  │ mmap host files       │  │
│ └────────┬────────┘  │ into guest address     │  │
│          │           │ space directly         │  │
│ ┌────────▼────────┐  └───────────────────────┘  │
│ │ PassthroughFs   │                              │
│ │ macOS syscalls  │  ┌───────────────────────┐  │
│ │ + FSEvents      │  │ FSEvents Monitor      │  │
│ │   monitor       ├──┤ FUSE_NOTIFY_INVAL     │  │
│ └─────────────────┘  │ → guest cache flush   │  │
│                      └───────────────────────┘  │
└─────────────────────────────────────────────────┘
         ↕ virtio queues + shared memory
┌─────────────────────────────────────────────────┐
│ Linux Guest                                      │
│ CONFIG_VIRTIO_FS + CONFIG_FUSE_FS               │
│ mount -t virtiofs -o cache=always tag /mnt      │
│ DAX: mmap → direct access to host pages         │
└─────────────────────────────────────────────────┘
```

## Phases

### Phase 1: fuse crate macOS compilation (2-3 days)

**Objective**: crosvm's fuse crate compiles on macOS.

**Expected Results**:
1. Remove `#![cfg(any(target_os = "android", target_os = "linux"))]` from fuse/src/lib.rs
2. Add `#[cfg(target_os = "macos")]` alternatives for Linux-specific types in sys.rs
3. cfg-gate mount.rs (not needed for virtio-fs path)
4. `cargo build` of fuse crate succeeds on macOS
5. server.rs and filesystem.rs compile without changes (protocol code is platform-agnostic)

**Dependencies**: None
**Risks**: sys.rs type definitions may have unexpected Linux assumptions

### Phase 2: passthrough.rs macOS port (1-2 weeks)

**Objective**: PassthroughFs works on macOS with basic file operations.

**Expected Results**:
1. All 15 Linux-specific APIs replaced with macOS equivalents:
   - openat64 → openat, fstatat64 → fstatat, stat64 → stat (p9 pattern)
   - /proc/self/fd/N → /dev/fd/N + fcntl(F_GETPATH) (p9 pattern)
   - O_PATH → O_RDONLY (p9 pattern)
   - getdents64 → readdir (p9 pattern)
   - renameat2 → renameatx_np (new)
   - copy_file_range → fcopyfile (new)
   - SYS_setresuid/setresgid → stub (no macOS equivalent)
   - prctl/unshare → stub
   - SELinux /proc/.../fscreate → remove
   - minijail → skip (FakeMinijailStub)
2. worker.rs: remove prctl(PR_SET_SECUREBITS) and unshare(CLONE_FS)
3. read_dir.rs: port getdents64 to readdir (identical to p9 pattern)
4. Guest can mount virtiofs and read/write files
5. All basic operations work: open, read, write, stat, mkdir, unlink, rename, symlink

**Dependencies**: Phase 1
**Risks**: O_PATH removal may cause subtle path traversal issues (mitigated by p9 experience)

### Phase 3: cache=always + device registration (3-5 days)

**Objective**: virtiofs device registered in crosvm macOS runner with cache=always.

**Expected Results**:
1. Fs device registered in src/crosvm/sys/macos/mod.rs (like 9p/vsock/gpu)
2. Guest boots and sees virtiofs device
3. Guest can `mount -t virtiofs -o cache=always host_share /mnt`
4. Second access to same file serves from guest page cache (no round-trip)
5. Performance test: sequential read of 100MB file ≥ 70% native

**Dependencies**: Phase 2

### Phase 4: DAX shared memory window (2-3 weeks)

**Objective**: Guest can mmap host files directly via DAX, enabling gcc/cargo/ld workloads.

**Expected Results**:
1. PCI BAR #4 configured as 8GB shared memory region
2. FUSE_SETUPMAPPING / FUSE_REMOVEMAPPING handled by passthrough
3. Guest mmap of virtiofs file → direct access to host page (zero-copy)
4. gcc compilation on virtiofs mount works (mmap of .h files)
5. Performance test: gcc compile of medium project ≥ 60% native

**Dependencies**: Phase 3
**Risks**: DAX was x86_64-only in crosvm. ARM64 DAX needs investigation.
May need HVF memory mapping (hv_vm_map) for the shared region.

### Phase 5: FSEvents cache invalidation (1-2 weeks) [DEPRECATED]

**Objective**: Host file changes propagate to guest immediately.

**Expected Results** [DEPRECATED — see Phase 5-revised]:
1. macOS FSEvents API monitors shared directory
2. On file change: send FUSE_NOTIFY_INVAL_INODE / FUSE_NOTIFY_INVAL_ENTRY to guest
3. Guest kernel clears cached metadata/data for changed files
4. Edit file on macOS → `cat` in guest sees update within 100ms
5. `webpack --watch` style workflows functional

**Deprecation reason**: Linux kernel virtiofs driver does NOT support receiving
FUSE_NOTIFY_INVAL_INODE/ENTRY notifications on the hiprio virtio queue.
The FUSE notification protocol types exist in the crate (NotifyOpcode,
NotifyInvalInodeOut, etc.) but the kernel-side handler was never implemented
for virtiofs transport (only for /dev/fuse). This is a known gap in the
virtiofs specification implementation. Expected results 2-4 are not achievable
without kernel modifications.

### Phase 5-revised: FSEvents + adaptive cache timeouts

**Objective**: Host file changes propagate to guest with minimal latency,
using adaptive cache timeout management driven by macOS FSEvents API.

**Architecture**:
- Cache policy: `cache=auto` (not `always`) with default timeout=30s
- FSEvents monitors shared directory with file-level granularity (100ms latency)
- Changed files are tracked as (dev, ino) pairs in a shared stale set
- GETATTR/LOOKUP for stale inodes returns timeout=0, forcing immediate revalidation
- Unchanged files remain cached for 30s (excellent read performance)
- Net effect: 30s cache for stable files, <1s revalidation for actively-edited files

**Expected Results**:
1. FSEvents monitor watches shared directory using kFSEventStreamCreateFlagFileEvents
2. Changed files tracked as stale (dev, ino) pairs in PassthroughFs
3. GETATTR for stale inodes returns attr_timeout=0 (forces revalidation on next access)
4. LOOKUP for stale parent dirs returns entry_timeout=0
5. Default timeout=30s for unchanged files (high cache hit rate)
6. Edit file on macOS → guest sees update within timeout window (max 30s, typically <1s
   for files being actively worked on since their previous cache already expired)
7. Build verified: `cargo check -p devices` passes with 0 errors

**Dependencies**: Phase 3
**Risks**:
- FSEvents has 100ms minimum coalesce latency (acceptable for this use case)
- Files deleted on host cannot be resolved by stat → handled by parent dir invalidation
- Pipe buffer overflow under extreme event rates → non-blocking writes, events dropped gracefully

### Phase 6: Performance tuning + benchmarks (1-2 weeks)

**Objective**: Optimize and measure against competitors.

**Expected Results**:
1. Benchmark suite: fio (IOPS), dd (throughput), npm install, gcc compile
2. Results documented vs Docker Desktop, Lima, native
3. Tune: readahead size, writeback mode, attribute timeout, queue depth
4. Target: ≥70% native for common developer workloads

**Dependencies**: Phase 5
