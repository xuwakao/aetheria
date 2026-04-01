# Progress: virtiofs on macOS

Created: 2026-04-01
Source: [plan/virtiofs-macos]

## Log

### [2026-04-01T14:00] META-PHASE A — Planning
Deep research complete. Key findings:
- virtio-fs does NOT need /dev/fuse or macFUSE on host (pure userspace FUSE server)
- 15 Linux APIs need macOS equivalents, 6 already done in p9 port
- libkrun validates this approach on macOS/HVF
- Target: 70-85% native with DAX + cache=always + FSEvents

### [2026-04-01T14:00] META-PHASE B — Plan Review

| # | Item | Verdict | Evidence |
|---|------|---------|----------|
| 1 | Dependency validation | PASS | Linear: 1→2→3→4→5→6. No cycles. |
| 2 | Expected results | PASS | Each phase has measurable criteria. |
| 3 | Phase 1 feasibility | PASS | Just remove cfg gate + type compat. p9 pattern proven. |
| 4 | Phase 2 feasibility | PASS | 6/15 APIs already done in p9. Rest are stub or simple. |
| 5 | Phase 3 feasibility | PASS | Device registration follows existing blk/net/vsock/9p/gpu pattern. |
| 6 | Phase 4 feasibility | RISK | DAX was x86_64-only. ARM64 needs investigation. hv_vm_map for shared region. |
| 7 | Phase 5 feasibility | PASS | FSEvents API well-documented. FUSE notify protocol standard. |
| 8 | Phase 6 feasibility | PASS | Benchmarking is straightforward. |

### [2026-04-01T16:00] Phase 4: DAX plumbing fix
- Replaced `mem::forget(fs_tube_host)` with `fs_mapping_handler_thread`
- Thread processes FsMappingRequest: AllocateSharedMemoryRegion, CreateMemoryMapping, RemoveMemoryMapping
- SystemAllocator wrapped in Arc<Mutex<>> for sharing between MSI and FS handler threads
- Build verified: `cargo check -p devices` — 0 errors

### [2026-04-02T00:00] Phase 5: FSEvents + adaptive cache invalidation
Plan corrected: FUSE_NOTIFY not supported by Linux kernel virtiofs driver.
Implemented adaptive timeout approach instead:
- Created `devices/src/virtio/fs/fsevents.rs` (300 LOC): macOS FSEvents monitor via CoreServices FFI
  - FSEventStreamCreate with kFSEventStreamCreateFlagFileEvents + kFSEventStreamCreateFlagNoDefer
  - Callback writes paths to pipe → reader thread stat()s → stale set
  - Non-blocking pipe writes, graceful event dropping under load
- Modified `passthrough.rs`: stale_inodes Arc<Mutex<HashSet<InodeAltKey>>>
  - do_getattr: returns timeout=0 for stale inodes, clears stale flag
  - do_lookup (both paths): same adaptive timeout logic
  - InodeAltKey made pub(crate) + Hash for HashSet usage
- Changed cache policy: Always → Auto with timeout=30s
- FsEventsMonitor lives in Fs device (not PassthroughFs) to avoid Sync issues
- Build verified: `cargo check -p devices` — 0 errors, 82 warnings (all pre-existing)

## Plan Corrections

### [2026-04-02T00:00] Phase 5 deprecated and replaced
Original Phase 5 relied on FUSE_NOTIFY_INVAL_INODE/ENTRY sent via hiprio queue.
Research found: Linux kernel virtiofs driver does NOT support receiving device-initiated
notifications on the hiprio queue. The FUSE notification protocol types exist in the
fuse crate (NotifyOpcode, NotifyInvalInodeOut) but the kernel transport was never implemented.
Replaced with adaptive timeout approach: FSEvents → stale set → timeout=0 on GETATTR/LOOKUP.
See [plan/virtiofs-macos#Phase5-revised].

## Findings

### F-001: FUSE_NOTIFY not supported in virtiofs transport
Linux kernel virtiofs driver (fs/fuse/virtio_fs.c) does not handle device-initiated
FUSE_NOTIFY messages on the hiprio queue. The hiprio queue processes completed requests
(responses to driver-initiated requests), not unsolicited notifications from the device.
FUSE notification infrastructure exists in fs/fuse/dev.c (fuse_notify()) but is only
connected to the /dev/fuse transport, not the virtio transport. This is a known gap.

### F-002: cache=always prevents all revalidation
With cache=always, the FUSE client sets FOPEN_KEEP_CACHE on open and uses infinite
attr/entry timeouts. There is no mechanism to force revalidation without FUSE_NOTIFY.
cache=auto with finite timeouts is the standard approach used by Docker Desktop, Lima,
and all other container runtimes for shared directories.

### F-003: FSEvents minimum latency is 100ms
FSEventStreamCreate latency parameter minimum is 0.1 seconds. Combined with
kFSEventStreamCreateFlagNoDefer, events are delivered at leading edge (near-immediate
for sparse changes). File-level granularity requires kFSEventStreamCreateFlagFileEvents.
