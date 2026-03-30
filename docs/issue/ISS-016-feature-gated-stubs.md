# ISS-016: Feature-gated device stubs (net, vsock, vhost) remain as placeholders

Created: 2026-03-31
Status: OPEN
Severity: LOW
Source: Full codebase review (2026-03-31)

## Description

Several macOS device stub files exist behind Cargo feature gates. They are never compiled with `--no-default-features` but would need real implementations if the features are enabled.

## Inventory

| File | Feature Gate | Current State | Needed For |
|------|-------------|---------------|------------|
| `devices/src/virtio/net/sys/macos.rs` | `net` | Empty (1-line comment) | VM networking |
| `devices/src/virtio/vsock/sys/macos.rs` | Always compiled | Minimal struct stubs | Host-guest sockets |
| `devices/src/virtio/vhost_user_backend/*/sys/macos.rs` (6 files) | Various features | Empty stubs | vhost-user backends |
| `devices/src/virtio/vhost_user_frontend/sys/macos.rs` | Always compiled | Empty | vhost-user frontend |
| `devices/src/virtio/console/sys/macos.rs` | Always compiled | **REAL** — kqueue WaitContext read loop | Serial console input |
| `devices/src/virtio/iommu/sys/macos.rs` | Always compiled | `futures::future::pending()` | VFIO passthrough |
| `vm_memory/src/udmabuf/sys/macos.rs` | Always compiled | Returns UdmabufUnsupported | DMA buffer sharing |

## Intentional Stubs (Platform Limitations)

These are correctly stubbed because the underlying feature doesn't exist on macOS:
- `devices/src/sys/macos/acpi.rs` — macOS has no ACPI event subsystem
- `vm_memory/src/udmabuf/sys/macos.rs` — Linux kernel feature
- `devices/src/virtio/iommu/sys/macos.rs` — VFIO passthrough requires kernel support

## Stubs Needing Real Implementation

These need real implementations when the corresponding feature is enabled:
- `net/sys/macos.rs` — requires `vmnet.framework`
- `vsock/sys/macos.rs` — requires custom socket implementation
- `vhost_user_backend/*/sys/macos.rs` — requires vhost-user protocol

## Recommended Fix

No immediate action. Track each as a separate feature request when networking/vsock/vhost support is needed.

## Findings
