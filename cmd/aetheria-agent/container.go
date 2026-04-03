//go:build linux

// container.go — Linux container lifecycle using namespaces.
//
// Creates isolated containers using unshare + pivot_root (chroot fallback).
// Each container has its own PID, mount, UTS, IPC, and (optionally) network
// namespace. Cgroups v2 resource limits (CPU, memory, PIDs) are applied
// per container. Port forwarding tunnels traffic via vsock.

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Storage paths — set by detectStorageMode() at startup.
var (
	containersDir = "/var/aetheria/containers"
	imagesDir     = "/mnt/host/aetheria-data/images"
	storageMode   = "rootfs"
)

// detectStorageMode picks the best storage backend:
//   disk   → /dev/vdb mounted at /mnt/data (dedicated sparse virtual disk, recommended)
//   rootfs → /var/aetheria on the system ext4 (simple, limited by rootfs size)
//   tmpfs  → /tmp/aetheria (fast, volatile, limited by RAM)
//
// Image tarballs are always cached on virtiofs (host filesystem, terabytes).
// Extracted rootfs and container overlayfs upper/work go to the selected backend.
func detectStorageMode() {
	// Try to mount dedicated data disk (/dev/vdb → /mnt/data).
	if _, err := os.Stat("/dev/vdb"); err == nil {
		os.MkdirAll("/mnt/data", 0755)
		if err := syscall.Mount("/dev/vdb", "/mnt/data", "ext4", 0, ""); err == nil {
			storageMode = "disk"
			containersDir = "/mnt/data/containers"
			imagesDir = "/mnt/host/aetheria-data/images"
		} else if _, err := os.Stat("/mnt/data/containers"); err == nil {
			// Already mounted (e.g., from fstab).
			storageMode = "disk"
			containersDir = "/mnt/data/containers"
			imagesDir = "/mnt/host/aetheria-data/images"
		} else {
			log.Printf("[storage] /dev/vdb mount failed: %v, falling back to rootfs", err)
		}
	}

	// Fallback: use system ext4 rootfs.
	if storageMode != "disk" {
		storageMode = "rootfs"
		containersDir = "/var/aetheria/containers"
		imagesDir = "/mnt/host/aetheria-data/images"
	}

	os.MkdirAll(containersDir, 0755)
	os.MkdirAll(imagesDir, 0755)
	log.Printf("[storage] mode=%s containers=%s images=%s", storageMode, containersDir, imagesDir)
}

// Container tracks a running container's state.
type Container struct {
	Name      string         `json:"name"`
	Rootfs    string         `json:"rootfs"`
	Pid       int            `json:"pid"`
	Status    string         `json:"status"` // "created", "running", "stopped"
	Image     string         `json:"image"`
	Network   NetworkMode    `json:"network"`
	IP        string         `json:"ip,omitempty"`    // bridge mode: assigned IP
	Ports     []PortMapping  `json:"ports,omitempty"` // port forwards
	Resources ResourceLimits `json:"resources,omitempty"`
	Restart   string         `json:"restart,omitempty"` // "no" (default), "always"
	cmd       *exec.Cmd
}

// PortMapping describes a host:container port forward.
type PortMapping struct {
	HostPort      uint16 `json:"host_port"`
	ContainerPort uint16 `json:"container_port"`
	Protocol      string `json:"protocol,omitempty"` // "tcp" (default), "udp"
}

// ContainerManager manages container lifecycle.
type ContainerManager struct {
	mu         sync.Mutex
	containers map[string]*Container
}

// ContainerConfig is the persistent subset of Container, saved to disk.
// Excludes runtime-only fields (Pid, cmd, IP).
type ContainerConfig struct {
	Name      string         `json:"name"`
	Image     string         `json:"image"`
	Network   NetworkMode    `json:"network"`
	Ports     []PortMapping  `json:"ports,omitempty"`
	Resources ResourceLimits `json:"resources,omitempty"`
	Restart   string         `json:"restart,omitempty"` // "no" (default), "always"
}

func NewContainerManager() *ContainerManager {
	detectStorageMode()
	initCgroupHierarchy()
	cm := &ContainerManager{
		containers: make(map[string]*Container),
	}
	cm.loadConfigs()
	cm.autoRestart()
	return cm
}

// autoRestart starts containers with restart=always that were restored from disk.
func (cm *ContainerManager) autoRestart() {
	cm.mu.Lock()
	var toStart []string
	for name, c := range cm.containers {
		if c.Restart == "always" && c.Status == "stopped" {
			toStart = append(toStart, name)
		}
	}
	cm.mu.Unlock()

	for _, name := range toStart {
		log.Printf("[persist] auto-restarting container %s", name)
		if err := cm.Start(name); err != nil {
			log.Printf("[persist] auto-restart %s failed: %v", name, err)
		}
	}
}

// saveConfig writes the container's config to <containersDir>/<name>/config.json.
// Uses atomic temp+rename to prevent corruption on crash.
// Must be called with cm.mu held.
func (cm *ContainerManager) saveConfig(c *Container) {
	cfg := ContainerConfig{
		Name:      c.Name,
		Image:     c.Image,
		Network:   c.Network,
		Ports:     c.Ports,
		Resources: c.Resources,
		Restart:   c.Restart,
	}

	dir := filepath.Join(containersDir, c.Name)
	os.MkdirAll(dir, 0755)

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		log.Printf("[persist] marshal config for %s: %v", c.Name, err)
		return
	}

	tmpPath := filepath.Join(dir, "config.json.tmp")
	finalPath := filepath.Join(dir, "config.json")

	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		log.Printf("[persist] write config for %s: %v", c.Name, err)
		return
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		log.Printf("[persist] rename config for %s: %v", c.Name, err)
		os.Remove(tmpPath)
		return
	}

	log.Printf("[persist] saved config for %s", c.Name)
}

// loadConfigs scans containersDir for saved config.json files and
// restores containers into the in-memory map with status "stopped".
func (cm *ContainerManager) loadConfigs() {
	entries, err := os.ReadDir(containersDir)
	if err != nil {
		log.Printf("[persist] scan containers dir: %v", err)
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		cfgPath := filepath.Join(containersDir, entry.Name(), "config.json")
		data, err := os.ReadFile(cfgPath)
		if err != nil {
			continue // no config.json — not a persisted container
		}

		var cfg ContainerConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			log.Printf("[persist] corrupt config for %s: %v (skipping)", entry.Name(), err)
			continue
		}

		// Use directory name as authoritative — handles edge case where
		// config.json Name doesn't match the directory.
		dirName := entry.Name()
		if cfg.Name != dirName {
			log.Printf("[persist] config name %q doesn't match directory %q, using directory name", cfg.Name, dirName)
			cfg.Name = dirName
		}

		// Reconstruct container from config.
		rootfs := filepath.Join(containersDir, cfg.Name, "rootfs")
		if _, err := os.Stat(rootfs); os.IsNotExist(err) {
			log.Printf("[persist] rootfs missing for %s (skipping)", cfg.Name)
			continue
		}

		cm.containers[cfg.Name] = &Container{
			Name:      cfg.Name,
			Rootfs:    rootfs,
			Status:    "stopped",
			Image:     cfg.Image,
			Network:   cfg.Network,
			Ports:     cfg.Ports,
			Resources: cfg.Resources,
			Restart:   cfg.Restart,
		}
		log.Printf("[persist] restored container %s (image=%s, restart=%s)", cfg.Name, cfg.Image, cfg.Restart)
	}

	if len(cm.containers) > 0 {
		log.Printf("[persist] restored %d containers", len(cm.containers))
	}
}

// ── RPC parameter types ──

// NetworkMode controls container network isolation.
//   - "host":   share VM network namespace (no isolation, default)
//   - "bridge": own net namespace + veth pair + br0 bridge + NAT
//   - "none":   own net namespace with no connectivity
type NetworkMode string

const (
	NetHost   NetworkMode = "host"
	NetBridge NetworkMode = "bridge"
	NetNone   NetworkMode = "none"
)

type ContainerCreateParams struct {
	Name      string         `json:"name"`
	Rootfs    string         `json:"rootfs"`    // path to rootfs directory
	Image     string         `json:"image"`     // image name (for display)
	Network   NetworkMode    `json:"network"`   // "host", "bridge", "none" (default: "bridge")
	Ports     []PortMapping  `json:"ports,omitempty"`
	Resources ResourceLimits `json:"resources,omitempty"`
	Restart   string         `json:"restart,omitempty"` // "no" (default), "always"
}

type ContainerExecParams struct {
	Name string   `json:"name"`
	Cmd  string   `json:"cmd"`
	Args []string `json:"args,omitempty"`
}

type ContainerNameParams struct {
	Name string `json:"name"`
}

// ── Container Lifecycle ──

func validateContainerName(name string) error {
	if name == "" {
		return fmt.Errorf("container name is empty")
	}
	if strings.ContainsAny(name, "/\\. \t\n") {
		return fmt.Errorf("container name %q contains invalid characters", name)
	}
	if len(name) > 64 {
		return fmt.Errorf("container name too long (max 64)")
	}
	return nil
}

func (cm *ContainerManager) Create(params ContainerCreateParams) error {
	if err := validateContainerName(params.Name); err != nil {
		return err
	}

	// Check for duplicates under lock, then release before slow operations.
	cm.mu.Lock()
	if _, exists := cm.containers[params.Name]; exists {
		cm.mu.Unlock()
		return fmt.Errorf("container %q already exists", params.Name)
	}
	cm.mu.Unlock()

	// Prepare rootfs OUTSIDE the lock — this may download images (slow).
	rootfs := params.Rootfs
	if rootfs == "" && params.Image != "" {
		var err error
		rootfs, err = PrepareContainerRootfs(params.Name, params.Image)
		if err != nil {
			return fmt.Errorf("prepare rootfs from image %q: %w", params.Image, err)
		}
	} else if rootfs == "" {
		rootfs = filepath.Join(containersDir, params.Name, "rootfs")
	}
	if !filepath.IsAbs(rootfs) {
		return fmt.Errorf("rootfs must be an absolute path: %s", rootfs)
	}

	if _, err := os.Stat(rootfs); os.IsNotExist(err) {
		return fmt.Errorf("rootfs not found: %s", rootfs)
	}

	// Re-acquire lock and insert (re-check for race).
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if _, exists := cm.containers[params.Name]; exists {
		return fmt.Errorf("container %q already exists", params.Name)
	}

	netMode := params.Network
	if netMode == "" {
		netMode = NetBridge // default: isolated bridge networking
	}

	restart := params.Restart
	if restart == "" {
		restart = "no"
	}
	if restart != "no" && restart != "always" {
		return fmt.Errorf("invalid restart policy %q (must be \"no\" or \"always\")", restart)
	}

	c := &Container{
		Name:      params.Name,
		Rootfs:    rootfs,
		Status:    "created",
		Image:     params.Image,
		Network:   netMode,
		Ports:     params.Ports,
		Resources: params.Resources,
		Restart:   restart,
	}
	cm.containers[params.Name] = c
	cm.saveConfig(c)

	log.Printf("[container] created %q (rootfs=%s)", params.Name, rootfs)
	return nil
}

func (cm *ContainerManager) Start(name string) error {
	cm.mu.Lock()
	c, ok := cm.containers[name]
	if !ok {
		cm.mu.Unlock()
		return fmt.Errorf("container %q not found", name)
	}
	if c.Status == "running" {
		cm.mu.Unlock()
		return fmt.Errorf("container %q already running", name)
	}
	image := c.Image
	rootfs := c.Rootfs
	cm.mu.Unlock()

	// Re-mount overlayfs if needed (lost after VM restart).
	// PrepareContainerRootfs is idempotent: skips if rootfs/bin already exists.
	// Done outside lock — extraction can be slow.
	if image != "" {
		if _, err := os.Stat(filepath.Join(rootfs, "bin")); os.IsNotExist(err) {
			log.Printf("[container] rootfs empty for %q, re-mounting overlayfs", name)
			if _, err := PrepareContainerRootfs(name, image); err != nil {
				return fmt.Errorf("re-mount rootfs for %q: %w", name, err)
			}
		}
	}

	cm.mu.Lock()
	// Re-check state after re-acquiring lock — container may have been
	// removed or started by a concurrent operation.
	if cm.containers[name] != c {
		cm.mu.Unlock()
		return fmt.Errorf("container %q was modified during start", name)
	}
	if c.Status == "running" {
		cm.mu.Unlock()
		return fmt.Errorf("container %q already running", name)
	}

	// Prepare container rootfs: ensure /proc, /sys, /dev, /tmp exist
	for _, dir := range []string{"proc", "sys", "dev", "tmp", "etc", "root"} {
		os.MkdirAll(filepath.Join(c.Rootfs, dir), 0755)
	}

	// Create the container init process with appropriate namespace flags.
	cmd := reexecContainer(c.Rootfs, name, c.Network)
	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		cm.mu.Unlock()
		return fmt.Errorf("failed to start container: %w", err)
	}

	c.Pid = cmd.Process.Pid
	c.Status = "running"
	c.cmd = cmd

	// Apply cgroup resource limits (after process starts, before it does real work).
	if !c.Resources.IsEmpty() {
		if err := setupCgroup(name, c.Pid, c.Resources); err != nil {
			log.Printf("[container] cgroup setup failed: %v (non-fatal)", err)
		}
	}

	// For bridge mode: set up veth pair + bridge after the container starts
	// (need the PID for moving veth into the container's net namespace).
	if c.Network == NetBridge {
		ip, err := setupBridgeNetwork(name, c.Pid)
		if err != nil {
			log.Printf("[container] bridge network setup failed: %v", err)
			// Non-fatal: container runs without network
		} else {
			c.IP = ip
		}
	}

	cm.mu.Unlock()

	log.Printf("[container] started %q pid=%d", name, c.Pid)

	// Wait for container to exit in background.
	go func() {
		cmd.Wait()
		// Clean up bridge networking if applicable.
		if c.Network == NetBridge {
			teardownBridgeNetwork(name)
		}
		// Clean up cgroup.
		if !c.Resources.IsEmpty() {
			cleanupCgroup(name)
		}
		cm.mu.Lock()
		c.Status = "stopped"
		c.Pid = 0
		c.cmd = nil
		c.IP = ""
		cm.mu.Unlock()
		log.Printf("[container] %q exited", name)
	}()

	return nil
}

func (cm *ContainerManager) Stop(name string) error {
	cm.mu.Lock()
	c, ok := cm.containers[name]
	if !ok {
		cm.mu.Unlock()
		return fmt.Errorf("container %q not found", name)
	}
	if c.Status != "running" || c.cmd == nil {
		cm.mu.Unlock()
		return fmt.Errorf("container %q is not running", name)
	}
	pid := c.Pid
	cm.mu.Unlock()

	// Send SIGTERM, then SIGKILL after 5s if still alive.
	syscall.Kill(pid, syscall.SIGTERM)
	log.Printf("[container] sent SIGTERM to %q pid=%d", name, pid)

	go func() {
		time.Sleep(5 * time.Second)
		cm.mu.Lock()
		stillRunning := c.Status == "running" && c.Pid == pid
		cm.mu.Unlock()
		if stillRunning {
			log.Printf("[container] SIGKILL %q pid=%d (did not stop in 5s)", name, pid)
			syscall.Kill(pid, syscall.SIGKILL)
		}
	}()
	return nil
}

func (cm *ContainerManager) Remove(name string) error {
	cm.mu.Lock()
	c, ok := cm.containers[name]
	if !ok {
		cm.mu.Unlock()
		return fmt.Errorf("container %q not found", name)
	}
	if c.Status == "running" {
		cm.mu.Unlock()
		return fmt.Errorf("container %q is running, stop it first", name)
	}
	rootfs := c.Rootfs
	delete(cm.containers, name)
	cm.mu.Unlock()

	// Clean up filesystem: unmount overlayfs (if mounted) and remove container dir.
	syscall.Unmount(rootfs, syscall.MNT_DETACH) // ignore error if not an overlay mount
	containerDir := filepath.Join(containersDir, name)
	if err := os.RemoveAll(containerDir); err != nil {
		log.Printf("[container] warning: failed to clean up %s: %v", containerDir, err)
	}

	log.Printf("[container] removed %q", name)
	return nil
}

func (cm *ContainerManager) Exec(params ContainerExecParams) (ExecResult, error) {
	cm.mu.Lock()
	c, ok := cm.containers[params.Name]
	if !ok {
		cm.mu.Unlock()
		return ExecResult{}, fmt.Errorf("container %q not found", params.Name)
	}
	if c.Status != "running" {
		cm.mu.Unlock()
		return ExecResult{}, fmt.Errorf("container %q is not running", params.Name)
	}
	pid := c.Pid
	network := c.Network
	cm.mu.Unlock()

	// Verify the container process is still alive before nsenter.
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); os.IsNotExist(err) {
		return ExecResult{}, fmt.Errorf("container %q process (pid %d) no longer exists", params.Name, pid)
	}

	// Use nsenter to exec inside the container's namespaces.
	// Always enter PID, mount, UTS, IPC. Enter net namespace only if
	// the container has its own (bridge or none mode).
	args := []string{
		"-t", fmt.Sprintf("%d", pid),
		"-p", "-m", "-u", "-i",
	}
	if network == NetBridge || network == NetNone {
		args = append(args, "-n") // also enter network namespace
	}
	args = append(args, "--")
	if len(params.Args) == 0 {
		args = append(args, "/bin/sh", "-c", params.Cmd)
	} else {
		args = append(args, params.Cmd)
		args = append(args, params.Args...)
	}

	cmd := exec.Command("nsenter", args...)
	const maxOutput = 10 * 1024 * 1024
	var stdout, stderr limitedWriter
	stdout.limit = maxOutput
	stderr.limit = maxOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return ExecResult{}, fmt.Errorf("nsenter exec failed: %w", err)
		}
	}

	return ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}, nil
}

func (cm *ContainerManager) List() []Container {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	list := make([]Container, 0, len(cm.containers))
	for _, c := range cm.containers {
		list = append(list, Container{
			Name:      c.Name,
			Rootfs:    c.Rootfs,
			Pid:       c.Pid,
			Status:    c.Status,
			Image:     c.Image,
			Network:   c.Network,
			IP:        c.IP,
			Ports:     c.Ports,
			Resources: c.Resources,
			Restart:   c.Restart,
		})
	}
	return list
}

// ── Container init (re-exec pattern) ──

const containerInitArg = "__aetheria_container_init__"

// reexecContainer creates a command that re-execs the agent binary
// with namespace flags. The child process calls containerInit().
func reexecContainer(rootfs, hostname string, network NetworkMode) *exec.Cmd {
	cmd := exec.Command("/proc/self/exe", containerInitArg, rootfs, hostname)
	flags := syscall.CLONE_NEWPID |
		syscall.CLONE_NEWNS |
		syscall.CLONE_NEWUTS |
		syscall.CLONE_NEWIPC

	// bridge and none modes get their own network namespace.
	// host mode shares the VM's network namespace.
	if network == NetBridge || network == NetNone {
		flags |= syscall.CLONE_NEWNET
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: uintptr(flags),
	}
	return cmd
}

// containerInit is called inside the new namespace (child process).
// It sets up the container environment and execs the container init.
func containerInit() {
	if len(os.Args) < 4 || os.Args[1] != containerInitArg {
		return
	}
	rootfs := os.Args[2]
	hostname := os.Args[3]

	// Must lock OS thread for namespace operations.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Set hostname.
	syscall.Sethostname([]byte(hostname))

	// Prepare mount namespace.
	setupContainerMounts(rootfs)

	// Mount /proc, /sys, /dev inside the container rootfs.
	mountContainerFS(rootfs)

	// Enter the container rootfs. Try pivot_root first (secure, requires
	// rootfs on a real filesystem). Fall back to chroot (works on initramfs).
	if err := pivotRoot(rootfs); err != nil {
		log.Printf("[container-init] pivot_root failed (%v), using chroot", err)
		if err := syscall.Chroot(rootfs); err != nil {
			log.Fatalf("[container-init] chroot failed: %v", err)
		}
		os.Chdir("/")
	}

	// Container init: stay alive so nsenter can execute commands.
	// Don't exec a shell (it exits without tty). Instead, block in Go
	// using select{} (infinite wait). The process stays as PID 1 in the
	// container's PID namespace. Users interact via nsenter.
	log.Printf("[container-init] container ready, waiting for commands")

	// Handle SIGTERM for graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh
	log.Printf("[container-init] received signal, exiting")
}

// setupContainerMounts prepares the mount namespace for the container.
func setupContainerMounts(rootfs string) {
	syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, "")
}

// mountContainerFS mounts /proc, /sys, /dev, /tmp inside the container rootfs.
func mountContainerFS(rootfs string) {
	proc := filepath.Join(rootfs, "proc")
	os.MkdirAll(proc, 0755)
	syscall.Mount("proc", proc, "proc", 0, "")

	sys := filepath.Join(rootfs, "sys")
	os.MkdirAll(sys, 0755)
	syscall.Mount("sysfs", sys, "sysfs", 0, "")

	dev := filepath.Join(rootfs, "dev")
	os.MkdirAll(dev, 0755)
	syscall.Mount("devtmpfs", dev, "devtmpfs", 0, "")

	devpts := filepath.Join(rootfs, "dev", "pts")
	os.MkdirAll(devpts, 0755)
	syscall.Mount("devpts", devpts, "devpts", 0, "")

	tmp := filepath.Join(rootfs, "tmp")
	os.MkdirAll(tmp, 01777)
	syscall.Mount("tmpfs", tmp, "tmpfs", 0, "")
}

func pivotRoot(newRoot string) error {
	// pivot_root requires:
	// 1. newRoot must be a mount point (done by bind-mount in setupContainerMounts)
	// 2. putOld must be under newRoot
	// 3. newRoot and current root must be different filesystems/mounts

	oldRoot := filepath.Join(newRoot, ".old_root")
	if err := os.MkdirAll(oldRoot, 0700); err != nil {
		return fmt.Errorf("mkdir old_root: %w", err)
	}

	// pivot_root atomically: makes newRoot the new /, moves old / to oldRoot.
	if err := syscall.PivotRoot(newRoot, oldRoot); err != nil {
		return fmt.Errorf("pivot_root(%s, %s): %w", newRoot, oldRoot, err)
	}

	// Now we're inside the new root. Change cwd.
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("chdir /: %w", err)
	}

	// Unmount old root (now at /.old_root) with MNT_DETACH for lazy unmount.
	oldMountPath := "/.old_root"
	if err := syscall.Unmount(oldMountPath, syscall.MNT_DETACH); err != nil {
		log.Printf("[container-init] unmount old root: %v (non-fatal)", err)
	}

	// Clean up the mount point directory.
	os.RemoveAll(oldMountPath)
	return nil
}

// ── RPC handlers ──

func (cm *ContainerManager) HandleRPC(req Request) Response {
	switch req.Method {
	case "container.create":
		var params ContainerCreateParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return Response{Error: "invalid params: " + err.Error(), ID: req.ID}
		}
		if err := cm.Create(params); err != nil {
			return Response{Error: err.Error(), ID: req.ID}
		}
		return Response{Result: "created", ID: req.ID}

	case "container.start":
		var params ContainerNameParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return Response{Error: "invalid params: " + err.Error(), ID: req.ID}
		}
		if err := cm.Start(params.Name); err != nil {
			return Response{Error: err.Error(), ID: req.ID}
		}
		return Response{Result: "started", ID: req.ID}

	case "container.stop":
		var params ContainerNameParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return Response{Error: "invalid params: " + err.Error(), ID: req.ID}
		}
		if err := cm.Stop(params.Name); err != nil {
			return Response{Error: err.Error(), ID: req.ID}
		}
		return Response{Result: "stopped", ID: req.ID}

	case "container.remove":
		var params ContainerNameParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return Response{Error: "invalid params: " + err.Error(), ID: req.ID}
		}
		if err := cm.Remove(params.Name); err != nil {
			return Response{Error: err.Error(), ID: req.ID}
		}
		return Response{Result: "removed", ID: req.ID}

	case "container.exec":
		var params ContainerExecParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return Response{Error: "invalid params: " + err.Error(), ID: req.ID}
		}
		result, err := cm.Exec(params)
		if err != nil {
			return Response{Error: err.Error(), ID: req.ID}
		}
		return Response{Result: result, ID: req.ID}

	case "container.list":
		return Response{Result: cm.List(), ID: req.ID}

	default:
		return Response{Error: fmt.Sprintf("unknown container method: %s", req.Method), ID: req.ID}
	}
}

// isContainerInit checks if we were re-exec'd as a container init process.
func isContainerInit() bool {
	return len(os.Args) >= 2 && os.Args[1] == containerInitArg
}

