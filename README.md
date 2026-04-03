# Aetheria 以太之境

A lightweight Linux container runtime for macOS, powered by [crosvm](https://github.com/nickkuk/nicrosvm) + Apple Hypervisor.framework.

## Features

- **ARM64 Linux VM** on Apple Silicon via crosvm/HVF — boots in seconds
- **Container isolation** — PID/mount/UTS/IPC/network namespaces + pivot_root
- **Bridge networking** — per-container veth + nftables NAT (10.42.0.0/24)
- **Port forwarding** — `-p 8080:80` tunnels traffic via vsock
- **Cgroups v2** — CPU, memory, PID limits per container
- **Container persistence** — survives VM restart, optional `--restart=always`
- **overlayfs CoW** — shared base images, per-container writable layer
- **Interactive shell** — PTY over vsock, raw terminal mode
- **virtiofs + DAX** — near-native filesystem sharing with zero-copy mmap
- **Multiple distros** — Alpine, Ubuntu 24.04, Debian 12

## Quick Start

```bash
# Prerequisites: Docker (for rootfs build), Go 1.21+

# First run — builds agent + CLI automatically
./run.sh

# In another terminal:
./run.sh create alpine myapp
./run.sh shell myapp

# With resource limits and port forwarding
./run.sh create ubuntu web -p 8080:80 --memory=512m --cpus=1.0

# Auto-restart on VM boot
./run.sh create alpine svc --restart=always

# List containers
./run.sh ls

# Stop and remove
./run.sh rm myapp
```

## Architecture

```
macOS Host                          Linux VM (Alpine, crosvm/HVF)
┌─────────────────────┐            ┌──────────────────────────────┐
│ aetheria CLI        │            │ aetheria-agent (PID 1)       │
│   ↕ Unix socket     │            │   ↕ namespace isolation      │
│ aetheria daemon     │◄──vsock──►│ ┌────────┐ ┌────────┐        │
│   ↕ crosvm          │            │ │alpine  │ │ubuntu  │ ...    │
│   ↕ virtio-fs/blk   │            │ │(10.42. │ │(10.42. │        │
│   ↕ virtio-net      │            │ │ 0.2)   │ │ 0.3)   │        │
└─────────────────────┘            │ └────────┘ └────────┘        │
                                   │     ↕ br-aetheria + NAT      │
                                   └──────────────────────────────┘
```

## Project Structure

```
aetheria/
├── cmd/aetheria/          # Host CLI + daemon (macOS)
├── cmd/aetheria-agent/    # Guest agent (Linux ARM64)
│   ├── container.go       # Container lifecycle + persistence
│   ├── network.go         # Bridge networking + nftables
│   ├── cgroup.go          # Cgroups v2 resource limits
│   ├── portforward.go     # Port forwarding via vsock
│   ├── images.go          # Distro image management + overlayfs
│   ├── shell.go           # Interactive shell RPC
│   └── pty.go             # PTY allocation
├── aetheria-crosvm/       # crosvm fork with HVF backend (submodule)
├── aetheria-kernel/       # Custom Linux 6.12.15 kernel (submodule)
├── run.sh                 # One-click launcher
└── docs/                  # Design docs + status
```

## Commands

| Command | Description |
|---------|-------------|
| `run` | Start VM daemon |
| `create <distro> [name]` | Create + start container |
| `shell <name>` | Interactive shell |
| `exec <cmd>` | Execute in VM |
| `ls` | List containers |
| `rm <name>` | Stop + remove container |
| `pull <distro>` | Download distro image |
| `images` | List available images |
| `ping` | Health check |
| `stop` | Shutdown VM |

### Create Options

```
-p host:container          Port forwarding (repeatable)
--net=bridge|host|none     Network mode (default: bridge)
--memory=512m              Memory limit
--cpus=1.0                 CPU limit
--pids=1024                Max processes
--restart=always           Auto-restart on VM boot
```

## License

MIT
