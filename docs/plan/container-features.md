# Plan: Container Feature Set Completion

Created: 2026-04-03T02:00:00Z
Status: COMPLETED
Source: User request — "按你推荐，全部推进" (OCI images, volume mounts, env vars, logs, port forward restore)

## Task Description

Implement five features to bring the container runtime to production usability:
1. OCI image support — pull standard Docker/OCI images from registries
2. Environment variables — pass `-e KEY=VALUE` to containers
3. Volume mounts — bind host directories into containers via virtiofs
4. Container logs — capture and retrieve container stdout/stderr
5. Port forward auto-restore — daemon restores port forwards for auto-restarted containers

## Alternatives & Trade-offs

### OCI Image Pull

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| A. Minimal OCI client in Go | No external deps, full control, runs inside VM | Must implement token auth + manifest parsing + layer extraction + whiteout handling | **Selected** |
| B. Shell out to `skopeo` or `crane` | Battle-tested, handles edge cases | Adds ~20MB binary dependency to guest rootfs, must cross-compile ARM64 | Rejected |
| C. Use containerd's image pull | Most complete OCI support | Massive dependency (containerd + snapshotter), overkill | Rejected |

**Rationale**: A minimal OCI client (~300 LOC) is sufficient. We only need pull (not push), anonymous auth (Docker Hub public images), and layer extraction. The OCI Distribution API is simple HTTP + JSON.

### Layer Extraction Strategy

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| A. Flatten all layers into single rootfs | Simple, matches current model (one base rootfs dir) | No layer dedup, re-extracts everything on update | **Selected for v1** |
| B. overlayfs with multiple lower dirs | True layer caching, dedup across images | Complex: multi-layer overlayfs + whiteout passthrough | Future |

**Rationale**: Flattening layers into a single rootfs directory matches the current `imageExtractPath` pattern. Layer dedup is a v2 optimization.

## Phases

### Phase 1: OCI Image Pull — Registry Client

**Objective**: Implement OCI registry HTTP client with Docker Hub anonymous auth.

**Expected Results**:
- [ ] New file `cmd/aetheria-agent/oci.go` with:
  - `OCIRef` struct parsing `[registry/]repo[:tag]` (default registry: `registry-1.docker.io`, default tag: `latest`)
  - `getAuthToken(ref)` — fetches bearer token from `auth.docker.io/token`
  - `fetchManifest(ref, token)` — GET manifest, returns parsed `OCIManifest` struct
  - `fetchBlob(ref, digest, token, dest)` — streams blob to file with progress
- [ ] `OCIManifest` struct with Config and Layers descriptors
- [ ] Handles Docker Hub `library/` prefix for official images (e.g., `alpine` → `library/alpine`)
- [ ] Handles manifest list (fat manifest) — selects `linux/arm64` platform
- [ ] Builds: `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./cmd/aetheria-agent/`

**Dependencies**: None
**Risks**: Docker Hub rate limiting (100 pulls/6h anonymous). Manifest list vs single manifest detection.
**Status**: PENDING

### Phase 2: OCI Image Pull — Layer Extraction & Integration

**Objective**: Extract OCI layers into rootfs, integrate with existing image system.

**Expected Results**:
- [ ] `pullOCIImage(ref string)` — full pull pipeline: auth → manifest → layers → extract
- [ ] Layer extraction handles `.wh.` whiteout files (delete markers) and `.wh..wh..opq` (opaque directories)
- [ ] Layers extracted sequentially into a single rootfs directory (flatten approach)
- [ ] OCI images cached by digest: `<imagesDir>/oci/<repo>/<digest>/rootfs`
- [ ] `imageRegistry` updated: OCI images coexist with existing hardcoded distros
- [ ] `PullImage()` updated: if name not in hardcoded registry, try OCI pull
- [ ] `image.pull` RPC accepts OCI refs: `nginx`, `nginx:1.25`, `ghcr.io/owner/repo:tag`
- [ ] Builds successfully

**Dependencies**: Phase 1
**Risks**: Whiteout handling correctness. Large images (ubuntu:latest ~30MB compressed, ~80MB extracted).
**Status**: PENDING

### Phase 3: Environment Variables

**Objective**: Pass environment variables to container init process.

**Expected Results**:
- [ ] `ContainerCreateParams` extended with `Env []string` (format: `KEY=VALUE`)
- [ ] `Container` struct extended with `Env []string`
- [ ] `ContainerConfig` (persistence) includes `Env`
- [ ] `reexecContainer()` passes env vars to child process via `cmd.Env`
- [ ] `containerInit()` exports env vars before blocking on signal
- [ ] `nsenter` exec inherits container env (passed via `-e` or env file)
- [ ] CLI accepts `-e KEY=VALUE` (repeatable)
- [ ] Builds: both agent and CLI

**Dependencies**: None (parallel with Phase 1-2)
**Status**: PENDING

### Phase 4: Volume Mounts

**Objective**: Bind-mount host directories (via virtiofs share) into containers.

**Expected Results**:
- [ ] `ContainerCreateParams` extended with `Volumes []VolumeMount` (`HostPath`, `ContainerPath`, `ReadOnly`)
- [ ] `Container` struct extended with `Volumes`
- [ ] `ContainerConfig` includes `Volumes`
- [ ] `containerInit()` bind-mounts volumes after pivot_root: `mount --bind /mnt/host/<path> <containerPath>`
- [ ] Host paths resolved relative to virtiofs share mount (`/mnt/host/`)
- [ ] CLI accepts `-v host:container[:ro]` (repeatable)
- [ ] Read-only mount supported via `MS_RDONLY` flag
- [ ] Builds: both agent and CLI

**Dependencies**: None (parallel)
**Risks**: Path validation — must prevent container escape via `..` traversal. Virtiofs share root must be configured.
**Status**: PENDING

### Phase 5: Container Logs

**Objective**: Capture container stdout/stderr and provide retrieval via RPC.

**Expected Results**:
- [ ] Container init stdout/stderr redirected to log files: `<containersDir>/<name>/container.log`
- [ ] Log file rotation: max 10MB, rename to `.log.1` on rotation
- [ ] New RPC method `container.logs` returns last N lines (default 100)
- [ ] CLI command `aetheria logs <name>` with optional `-n` flag
- [ ] Builds: both agent and CLI

**Dependencies**: None (parallel)
**Status**: PENDING

### Phase 6: Port Forward Auto-Restore

**Objective**: After VM restart, daemon automatically restores port forwards for running containers.

**Expected Results**:
- [ ] Host daemon: after agent connects (ping success), queries `container.list`
- [ ] For each running container with ports, sends `_daemon.portforward.start`
- [ ] Existing port forward TCP listeners cleaned up before restore (idempotent)
- [ ] Builds: `go build ./cmd/aetheria/`

**Dependencies**: Phase 1-5 complete (final polish phase)
**Status**: PENDING

### Phase 7: Integration Review

**Objective**: Final code review and build verification.

**Expected Results**:
- [ ] Both binaries compile cleanly
- [ ] Code review: OCI client handles HTTP errors, redirects, timeouts
- [ ] Code review: whiteout handling correct
- [ ] Code review: env vars properly sanitized
- [ ] Code review: volume mounts prevent path traversal
- [ ] Code review: log rotation thread-safe
- [ ] `aetheria ls` shows all new fields (env, volumes, restart)

**Dependencies**: All prior phases
**Status**: PENDING

## Findings

