# Aetheria 以太之境

[中文文档](README_CN.md)

A cross-platform lightweight Linux container runtime, powered by [crosvm](https://github.com/xuwakao/crosvm) (forked from [Google crosvm](https://chromium.googlesource.com/crosvm/crosvm)) VMM. Currently supports macOS (Apple Hypervisor.framework), with Linux (KVM) and Windows (WHPX) planned.

## Why Aetheria?

Running Linux containers on non-Linux hosts requires a virtual machine. Existing solutions each have trade-offs:

| Solution | Limitation |
|----------|-----------|
| Docker Desktop | Heavy (~2GB), closed-source, licensing restrictions |
| OrbStack | macOS-only, closed-source |
| Lima/colima | QEMU-based, no GPU passthrough path |
| WSL2 | Windows-only, Microsoft-controlled kernel |
| Podman Machine | QEMU-based, limited macOS integration |

**Aetheria** aims to be an open-source, cross-platform alternative with:

- **Unified VMM** — crosvm runs on HVF (macOS), KVM (Linux), WHPX (Windows) with one codebase
- **Near-native I/O** — virtiofs with DAX zero-copy mmap achieves 30 GB/s read throughput
- **Lightweight** — Alpine-based VM boots in ~5 seconds, idles at ~30MB memory
- **GPU path** — crosvm's virtio-gpu + Rutabaga provides a roadmap to 3D acceleration

## Features

- **Container isolation** — PID/mount/UTS/IPC/network namespaces + pivot_root
- **Bridge networking** — per-container veth pair + nftables NAT (10.42.0.0/24)
- **Port forwarding** — `-p 8080:80` tunnels traffic via vsock
- **Cgroups v2** — CPU, memory, PID limits per container
- **Container persistence** — survives VM restart, optional `--restart=always`
- **overlayfs CoW** — shared base images, per-container writable layer
- **Interactive shell** — PTY over vsock, raw terminal mode
- **virtiofs + DAX** — near-native filesystem sharing with zero-copy mmap
- **Multiple distros** — Alpine 3.21, Ubuntu 24.04, Debian 12

## Architecture

```
Host (macOS / Linux / Windows)
┌──────────────────────────────────────────────────────┐
│  aetheria CLI + daemon (Go)                          │
│    → manages crosvm process                          │
│    → communicates with guest via vsock               │
│                                                      │
│  crosvm (Rust VMM, cross-platform):                  │
│    macOS:   HVF (Apple Hypervisor.framework)         │
│    Linux:   KVM                                      │
│    Windows: WHPX                                     │
│  Devices: virtio-blk, virtio-net, virtio-fs,         │
│           virtio-vsock, virtio-gpu (Rutabaga)        │
│                                                      │
│══════════════════ VM boundary ═══════════════════════│
│                                                      │
│  Single Linux VM (shared kernel model)               │
│    Custom kernel (mainline 6.12 + virtio + ns/cgroup)│
│    aetheria-agent (Go, vsock JSON-RPC service)       │
│                                                      │
│    ┌────────────┐ ┌────────────┐ ┌────────────┐     │
│    │ Container 1│ │ Container 2│ │ Container N│     │
│    │ Alpine     │ │ Ubuntu     │ │  ...       │     │
│    │ ns+cgroup  │ │ ns+cgroup  │ │ ns+cgroup  │     │
│    │ overlayfs  │ │ overlayfs  │ │ overlayfs  │     │
│    │ 10.42.0.2  │ │ 10.42.0.3  │ │ 10.42.0.x  │     │
│    └─────┬──────┘ └─────┬──────┘ └─────┬──────┘     │
│          └──────┬──br-aetheria──┬──────┘             │
│                 │  10.42.0.1/24 │                    │
│              nftables NAT masquerade                 │
│                      ↕                               │
│                 eth0 → internet                      │
└──────────────────────────────────────────────────────┘
```

### Communication Channels

| Channel | Transport | Purpose |
|---------|-----------|---------|
| Control RPC | vsock port 1024 | CLI → daemon → agent, JSON-RPC (create/start/stop/exec/...) |
| Shell stream | vsock port 1025 | Bidirectional PTY byte stream, raw terminal |
| Port forward | vsock port 1026 | Per-connection TCP tunnel, header-based multiplexing |
| Filesystem | virtio-fs + DAX | Host directory sharing, zero-copy mmap via `hv_vm_map` |
| Block storage | virtio-blk | Root disk (ext4) + data disk (64GB sparse, thin provisioned) |

## Quick Start

### Prerequisites

- Go 1.21+
- Rust toolchain (for building crosvm)
- Docker (for building ARM64 rootfs)
- macOS with Apple Silicon (current platform)

### Build

```bash
# Clone with submodules
git clone --recursive https://github.com/xuwakao/aetheria.git
cd aetheria

# 1. Build crosvm (one-time, ~5 min)
cd aetheria-crosvm
cargo build --release
cd ..

# 2. Build kernel (one-time, ~10 min)
cd aetheria-kernel
./build-kernel.sh arm64
./build-rootfs.sh arm64
cd ..

# 3. Build agent + CLI + install into rootfs (automatic on first ./run.sh)
./run.sh build
```

### Run

```bash
# Terminal 1: start VM daemon
./run.sh

# Terminal 2: create and enter a container
./run.sh create alpine myapp
./run.sh shell myapp

# With resource limits and port forwarding
./run.sh create ubuntu web -p 8080:80 --memory=512m --cpus=1.0

# Auto-restart on VM boot
./run.sh create alpine svc --restart=always

# List / remove
./run.sh ls
./run.sh rm myapp
```

## Commands

| Command | Description |
|---------|-------------|
| `run` | Start VM daemon (foreground) |
| `create <distro> [name]` | Create and start a container |
| `shell <name>` | Interactive shell in container |
| `exec <cmd>` | Execute command in VM |
| `ls` | List all containers |
| `rm <name>` | Stop and remove container |
| `pull <distro>` | Download distro rootfs image |
| `images` | List available/cached images |
| `ping` | Agent health check |
| `info` | Show VM information |
| `stop` | Shutdown VM |

### Create Options

```
-p host:container          Port forwarding (repeatable)
--net=bridge|host|none     Network mode (default: bridge)
--memory=512m              Memory limit (supports k/m/g)
--cpus=1.0                 CPU limit (fractional cores)
--pids=1024                Max number of processes
--restart=always           Auto-restart on VM boot
```

## Performance

Measured on Apple M-series, crosvm/HVF, Linux 6.12.15:

| Metric | Aetheria | QEMU virtiofsd (no DAX) | Note |
|--------|----------|------------------------|------|
| virtiofs read (DAX, cached) | 30 GB/s | 640 MB/s | DAX = host memory bandwidth, not disk I/O |
| virtiofs read (first access) | 3-7 GB/s | — | Limited by SSD, before page cache warms |
| virtiofs 4K write | 55 MB/s | 14 MB/s | Writes always go through FUSE protocol |
| virtio-blk sequential read | 22.5 GB/s | — | — |
| VM boot to shell | ~5.5s | — | — |

**How DAX works:** `hv_vm_map` maps host file pages directly into guest physical address space. Guest CPU reads host memory with no FUSE overhead — essentially memory bandwidth speed. This is the same approach used by QEMU/virtiofsd DAX and kata-containers.

**Trade-offs of DAX:**
- Stale reads — host file changes aren't instantly visible to guest (mitigated by FSEvents cache invalidation)
- Memory pressure — every DAX-mapped page uses real host physical memory
- Read-only benefit — writes still go through FUSE; metadata ops (stat, readdir) are FUSE speed

## Project Structure

```
aetheria/
├── cmd/aetheria/            # Host CLI + daemon
├── cmd/aetheria-agent/      # Guest agent (Linux ARM64)
│   ├── container.go         #   Container lifecycle + persistence
│   ├── network.go           #   Bridge networking + nftables NAT
│   ├── cgroup.go            #   Cgroups v2 resource limits
│   ├── portforward.go       #   Port forwarding via vsock tunnel
│   ├── images.go            #   Distro image management + overlayfs
│   ├── shell.go             #   Interactive shell RPC handler
│   └── pty.go               #   PTY allocation + nsenter
├── aetheria-crosvm/         # crosvm fork — HVF backend for macOS (submodule)
├── aetheria-kernel/         # Custom Linux 6.12.15 kernel configs (submodule)
├── run.sh                   # One-click launcher
├── scripts/                 # Build + install helpers
└── docs/                    # Design docs, plans, status reports
```

## Roadmap

- [x] crosvm HVF backend (Apple Hypervisor.framework)
- [x] Custom kernel (Linux 6.12.15, ARM64 + x86_64)
- [x] virtiofs + DAX zero-copy file sharing
- [x] Container runtime (namespaces + overlayfs)
- [x] Bridge networking + nftables NAT
- [x] Port forwarding (`-p host:container`)
- [x] Cgroups v2 resource isolation
- [x] Container persistence + auto-restart
- [ ] OCI image support (Docker Hub pull)
- [ ] AetheriaDisplay.app (Metal GPU rendering)
- [ ] Volume mounts (`-v host:container`)
- [ ] Linux KVM backend
- [ ] Windows WHPX backend

## Acknowledgements

- [crosvm](https://chromium.googlesource.com/crosvm/crosvm) — Chrome OS Virtual Machine Monitor by Google
- [Linux kernel](https://kernel.org) — custom-configured for lightweight VM use
- [Alpine Linux](https://alpinelinux.org) — minimal guest OS

## License

[MIT](LICENSE) — attribution required when redistributing.
