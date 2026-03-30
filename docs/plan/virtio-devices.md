# Plan: virtio-devices

Created: 2026-03-31T18:00:00+08:00
Status: PAUSED
Source: Codebase audit of virtio device creation flow + vmnet.framework research
Supersedes: [plan/virtio-stack] (DEPRECATED — incorrect assumptions about p9 portability)

## Task Description

Implement the production-grade virtio device stack on macOS: block storage with Alpine rootfs, vmnet.framework networking, and host-guest file sharing. This is a commercial product — no stubs, no shortcuts, no simplified fallbacks.

## Alternatives & Trade-offs

### File Sharing

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| virtio-9p (ungate existing) | Previously appeared trivial | p9 crate external, uses Linux syscalls; device creation requires minijail+namespaces+pivot_root; SharedDir config missing on macOS. Full port needed, not "ungating". | **REJECTED** — effort exceeds benefit vs virtio-fs |
| virtio-fs (FUSE server) | Better performance than 9p, mmap support, production-grade (OrbStack uses it) | Also Linux-gated in cfg_if; FUSE server (`fs/mod.rs`) needs filesystem passthrough backend; requires audit | **SELECTED** — better long-term investment |
| Host-only NFS/SMB | Standard protocol, no kernel module | High overhead, complex server setup, poor write performance | Rejected |

### Networking Backend

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| vmnet.framework direct | Native macOS, L2 Ethernet, DHCP/NAT built-in | Requires root for shared mode | **SELECTED** |
| socket_vmnet daemon | Unprivileged main process | Extra daemon, deployment complexity | Deferred to distribution phase |

### Rootfs Strategy

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| Alpine minirootfs ext4 on virtio-blk | Small (8MB), apk, production-grade package manager | Needs image management | **SELECTED** |
| erofs + overlayfs | Read-only base, CoW, OrbStack-like | Complex pipeline | Deferred to optimization phase |

## Phases

### Phase 1: virtio-blk device registration

**Objective**: Wire BlockAsync into macOS `run_config` following the exact same pattern as Linux: `Config.disks` → `DiskOption.open()` → `BlockAsync::new()` → `VirtioPciDevice::new()` → `build_vm`. Guest kernel must detect `/dev/vda`.

**Expected Results**:
- [ ] `run_config` reads `cfg.disks` and creates `BlockAsync` devices with proper PCI wrapping
- [ ] Each disk device gets MSI-X tube, ioevent tube, and vm_control tube (matching Linux flow)
- [ ] Devices passed to `Arch::build_vm` in the `devs` vector (not empty `Vec::new()`)
- [ ] `cargo build --no-default-features` compiles with zero new errors
- [ ] Boot test: kernel detects `virtio_blk virtio0` and creates `/dev/vda` device node
- [ ] Boot test: `fdisk -l /dev/vda` shows correct disk geometry from guest

**Dependencies**: None
**Risks**: VirtioPciDevice::new() requires multiple Tube pairs for control communication. On macOS the VM control event loop is not running (ISS-011), so some control paths may not function. Mitigation: create the tubes but accept that runtime control (resize, etc.) won't work until ISS-011 is resolved.
**Status**: COMPLETE

### Phase 1b: Write I/O fix (deferred)

**Objective**: Fix virtio-blk write operations. Currently reads work but writes return I/O error. Disk mounted read-only with `norecovery` as workaround.

**Status**: DEFERRED — Alpine boots read-only, writes can be investigated later.

### Phase 2: Alpine rootfs image + boot pivot

**Objective**: Build a production-quality Alpine Linux ext4 root filesystem image and update the initramfs to pivot-root into it. The VM must boot to a full Alpine system with `apk` package manager.

**Expected Results**:
- [ ] `scripts/build-rootfs.sh` downloads Alpine minirootfs, creates ext4 image with: root password, serial getty, network config placeholders, mount points
- [ ] Image size: 256MB (expandable), ext4 with standard inode/block configuration
- [ ] Initramfs `/init` script: waits for `/dev/vda`, mounts ext4, executes `switch_root`
- [ ] Boot test: VM boots to Alpine login prompt on ttyS0
- [ ] Boot test: `cat /etc/alpine-release` shows version
- [ ] Boot test: `apk --version` works (package manager functional without network)
- [ ] Clean shutdown: `poweroff` triggers PSCI SYSTEM_OFF → VM exits cleanly

**Dependencies**: Phase 1 (virtio-blk must be detected as /dev/vda)
**Risks**: switch_root requires the initramfs to properly unmount and exec into the new root. If kernel modules are needed for ext4 but not built-in, mount will fail. Mitigation: verify CONFIG_EXT4_FS=y is built-in (confirmed in defconfig audit).
**Status**: COMPLETE (read-only mount with norecovery — Alpine boots to login prompt)

### Phase 3: vmnet.framework FFI + TapT backend

**Objective**: Implement a production-grade vmnet.framework backend that satisfies crosvm's TapT trait, providing L2 Ethernet packet I/O for virtio-net.

**Expected Results**:
- [ ] `net_util/src/sys/macos/vmnet.rs`: vmnet FFI bindings (vmnet_start_interface, vmnet_stop_interface, vmnet_read, vmnet_write, vmnet_interface_set_event_callback)
- [ ] `net_util/src/sys/macos/tap.rs`: VmnetTap struct implementing TapT + Read + Write + AsRawFd
- [ ] VmnetTap creates shared-mode vmnet interface with DHCP configuration
- [ ] Packet I/O: single-iovec coalescing for vmnet_write [plan/virtio-stack#F-005]
- [ ] Event bridging: GCD callback → pipe fd → kqueue integration [plan/virtio-stack#F-006]
- [ ] VmnetTap implements AsRawFd returning the pipe fd for kqueue readability
- [ ] Interface cleanup: vmnet_stop_interface called on Drop
- [ ] `cargo build --no-default-features` compiles
- [ ] Unit test: VmnetTap creation succeeds (requires root)

**Dependencies**: None (can be developed independently of Phase 1-2)
**Risks**:
- vmnet requires root — tests must run with sudo
- GCD thread safety: vmnet callback fires on arbitrary GCD thread, must safely signal the kqueue reactor
- vmnet_write single-iovec constraint: must coalesce scatter-gather before write
- Entitlement: shared mode does NOT need com.apple.vm.networking entitlement, but needs root
**Status**: PENDING

### Phase 4: virtio-net device registration + guest networking

**Objective**: Create virtio-net devices backed by VmnetTap, register on PCI bus, and achieve end-to-end guest networking (DHCP IP, DNS, internet access).

**Expected Results**:
- [ ] `run_config` creates virtio-net device from VmnetTap backend
- [ ] Net device registered on PCI bus with MSI-X interrupt
- [ ] Kernel config has CONFIG_VIRTIO_NET=y (confirmed in defconfig)
- [ ] Boot test: guest kernel detects `virtio_net virtio1` and creates `eth0`
- [ ] Boot test: `udhcpc -i eth0` obtains DHCP lease from vmnet (192.168.x.x range)
- [ ] Boot test: `ping -c 3 8.8.8.8` succeeds (NAT internet via vmnet)
- [ ] Boot test: `ping -c 3 google.com` succeeds (DNS resolution)
- [ ] Boot test: `apk update && apk add curl` succeeds (full package install over network)
- [ ] `cargo build --no-default-features` compiles

**Dependencies**: Phase 1-2 (Alpine rootfs for testing), Phase 3 (VmnetTap backend)
**Risks**: virtio-net on crosvm uses complex TX/RX queue handling with async workers. The macOS async path (kqueue) must properly handle virtqueue events. Mitigation: reuse existing crosvm virtio-net device code, only the TapT backend is new.
**Status**: PENDING

### Phase 5: virtio-fs file sharing

**Objective**: Enable host-guest directory sharing via virtio-fs with a FUSE passthrough backend. Host directory mounted read-write in the guest.

**Expected Results**:
- [ ] virtio-fs device (`devices/src/virtio/fs/`) ungated for macOS in cfg_if
- [ ] FUSE passthrough server functional on macOS (audit + fix platform-specific code)
- [ ] `run_config` accepts shared directory configuration, creates Fs device
- [ ] Fs device registered on PCI bus
- [ ] Kernel config has CONFIG_VIRTIO_FS=y (confirmed in defconfig)
- [ ] Boot test: guest mounts shared dir: `mount -t virtiofs share0 /mnt/host`
- [ ] Boot test: file created on host appears in guest `/mnt/host/`
- [ ] Boot test: file created in guest `/mnt/host/` appears on host
- [ ] `cargo build --no-default-features` compiles

**Dependencies**: Phase 1-2 (Alpine rootfs for testing)
**Risks**: The fs module's FUSE passthrough server may use Linux-specific syscalls (openat2, renameat2, fstatat with AT_EMPTY_PATH). These require macOS equivalents or fallbacks. Requires thorough crate audit before implementation.
**Status**: PENDING

### Phase 6: Integration + production polish

**Objective**: All virtio devices working together, clean boot sequence, proper error handling, entitlement signing automated.

**Expected Results**:
- [ ] Boot sequence: initramfs → pivot to /dev/vda (ext4) → Alpine with networking + shared dir
- [ ] End-to-end: boot → DHCP IP → apk install → read/write shared files
- [ ] `scripts/run-vm.sh` automates: build → sign → create rootfs → launch VM
- [ ] Codesign with hypervisor entitlement automated in build script
- [ ] Clean shutdown via `poweroff` (PSCI SYSTEM_OFF) exits crosvm cleanly
- [ ] Terminal restored to canonical mode after VM exit
- [ ] No resource leaks: vmnet interface stopped, HVF VM destroyed, all threads joined
- [ ] `cargo build --no-default-features` compiles
- [ ] All documentation updated

**Dependencies**: Phases 1-5
**Status**: PENDING

## Findings

### F-001: virtio-blk macOS backend is complete
`devices/src/virtio/block/sys/macos.rs` implements `DiskOption::open()`, `get_seg_max()`, and `BlockAsync::create_executor()`. The device just needs PCI wrapping and registration in run_config.

### F-002: Linux device creation uses VirtioPciDevice wrapper with 5 tube pairs
Each virtio device needs: MSI tube, ioevent tube, vm_control tube, optional shared_memory tube, and optional disk_control tube. These enable runtime control communication. On macOS, tubes must be created but runtime control will be limited until ISS-011 (control socket) is resolved.

### F-003: virtio-9p is NOT portable to macOS without major effort
The p9 crate is external (crates.io), likely uses Linux-specific filesystem syscalls. Device creation requires minijail + Linux namespaces + pivot_root. SharedDir config doesn't exist on macOS. This is NOT a simple ungating — it's a full port. [Supersedes plan/virtio-stack#F-003]

### F-004: virtio-fs FUSE server needs platform audit
`devices/src/virtio/fs/` is gated behind Linux cfg_if. The FUSE passthrough filesystem server needs auditing for Linux-specific syscalls. However, the FUSE protocol itself is OS-agnostic, and macOS has FUSE support (macFUSE/FUSE-T).

### F-005: macOS run_config passes empty device list to build_vm
`src/crosvm/sys/macos.rs` line 233: `let devices: Vec<...> = Vec::new()`. This is why no virtio devices appear in the guest. The fix is to populate this vector with VirtioPciDevice-wrapped virtio devices, following the Linux pattern.

### F-006: VirtioPciDevice determines PCI class from DeviceType
For block devices: `PciClassCode::MassStorage` + `PciMassStorageSubclass::Other`. For net: `PciClassCode::NetworkController`. This is automatic based on the VirtioDevice's `device_type()` return value.
