# Plan: Container Persistence

Created: 2026-04-03T01:00:00Z
Status: COMPLETED
Source: Roadmap P1 — container state survives VM restart

## Task Description

Persist container configuration to disk so containers survive agent/VM restarts.
On agent startup, restore known containers from saved config files.

## Alternatives & Trade-offs

| Approach | Pros | Cons | Verdict |
|----------|------|------|---------|
| A. Per-container JSON config file | Simple, human-readable, atomic write via rename, easy to debug | One file per container (many files if hundreds of containers) | **Selected** |
| B. Single state database (SQLite/bbolt) | Single file, queryable | Adds dependency, overkill for <100 containers | Rejected |
| C. Single JSON array file | Simple | Concurrent writes need locking, corruption risk on crash | Rejected |

**Rationale**: Per-container `config.json` at `<containersDir>/<name>/config.json` is the Docker/OCI standard pattern. Each file is independently written via atomic rename. The container directory already exists (holds rootfs/upper/work).

## Phases

### Phase 1: Persist on Create, Restore on Startup

**Objective**: Save container config on create, load all saved configs when agent starts.

**Expected Results**:
- [ ] `ContainerConfig` struct (persistent subset of Container — excludes runtime fields like Pid, cmd, IP)
- [ ] `saveConfig(name)` writes `<containersDir>/<name>/config.json` via atomic temp+rename
- [ ] `loadConfigs()` scans `containersDir`, reads each `config.json`, populates `cm.containers` map with status "stopped"
- [ ] `NewContainerManager()` calls `loadConfigs()` after `detectStorageMode()`
- [ ] `Create()` calls `saveConfig()` after inserting into map
- [ ] `Remove()` deletes config.json along with container directory (already does `os.RemoveAll`)
- [ ] Builds: `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./cmd/aetheria-agent/`

**Dependencies**: None

**Status**: PENDING

### Phase 2: Update Config on State Changes + Restart Policy

**Objective**: Keep config.json in sync with runtime changes. Add optional auto-restart.

**Expected Results**:
- [ ] `ContainerCreateParams` extended with `Restart string` field ("no", "always"; default "no")
- [ ] `Container` struct extended with `Restart string` field
- [ ] Config saved/updated on: create, stop (to persist final status)
- [ ] On agent startup, containers with `restart=always` and status "running" (was running when VM died) are auto-started
- [ ] CLI `create` accepts `--restart=always` flag
- [ ] Builds: both agent and CLI

**Dependencies**: Phase 1

**Status**: PENDING

### Phase 3: Integration Verification

**Objective**: Code review and build verification.

**Expected Results**:
- [ ] Both binaries compile cleanly
- [ ] Code review: atomic writes prevent corruption
- [ ] Code review: loadConfigs handles missing/corrupt files gracefully
- [ ] Code review: no race between save and concurrent operations
- [ ] Container list shows restored containers correctly

**Dependencies**: Phase 1, 2

**Status**: PENDING

## Findings

