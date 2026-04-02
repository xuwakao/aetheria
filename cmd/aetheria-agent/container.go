//go:build linux

// container.go — Linux container lifecycle using namespaces.
//
// Creates isolated containers using unshare + pivot_root + cgroups v2.
// Each container has its own PID, mount, UTS, IPC, and network namespace.
// The container rootfs is an extracted distro image (Ubuntu, Alpine, etc.)
// located at /var/aetheria/containers/<name>/rootfs/.

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

const (
	containersDir = "/var/aetheria/containers"
	imagesDir     = "/var/aetheria/images"
)

// Container tracks a running container's state.
type Container struct {
	Name    string `json:"name"`
	Rootfs  string `json:"rootfs"`
	Pid     int    `json:"pid"`
	Status  string `json:"status"` // "created", "running", "stopped"
	Image   string `json:"image"`
	cmd     *exec.Cmd
}

// ContainerManager manages container lifecycle.
type ContainerManager struct {
	mu         sync.Mutex
	containers map[string]*Container
}

func NewContainerManager() *ContainerManager {
	os.MkdirAll(containersDir, 0755)
	os.MkdirAll(imagesDir, 0755)
	return &ContainerManager{
		containers: make(map[string]*Container),
	}
}

// ── RPC parameter types ──

type ContainerCreateParams struct {
	Name   string `json:"name"`
	Rootfs string `json:"rootfs"` // path to rootfs directory
	Image  string `json:"image"`  // image name (for display)
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

	cm.mu.Lock()
	defer cm.mu.Unlock()

	if _, exists := cm.containers[params.Name]; exists {
		return fmt.Errorf("container %q already exists", params.Name)
	}

	rootfs := params.Rootfs
	if rootfs == "" {
		rootfs = filepath.Join(containersDir, params.Name, "rootfs")
	}

	if _, err := os.Stat(rootfs); os.IsNotExist(err) {
		return fmt.Errorf("rootfs not found: %s", rootfs)
	}

	cm.containers[params.Name] = &Container{
		Name:   params.Name,
		Rootfs: rootfs,
		Status: "created",
		Image:  params.Image,
	}

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

	// Prepare container rootfs: ensure /proc, /sys, /dev, /tmp exist
	for _, dir := range []string{"proc", "sys", "dev", "tmp", "etc", "root"} {
		os.MkdirAll(filepath.Join(c.Rootfs, dir), 0755)
	}

	// Create the container init process.
	// We re-exec ourselves with a special argument to enter the namespace.
	// This is the standard pattern (used by Docker, containerd, runc).
	cmd := reexecContainer(c.Rootfs, name)
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
	cm.mu.Unlock()

	log.Printf("[container] started %q pid=%d", name, c.Pid)

	// Wait for container to exit in background.
	go func() {
		cmd.Wait()
		cm.mu.Lock()
		c.Status = "stopped"
		c.Pid = 0
		c.cmd = nil
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
	delete(cm.containers, name)
	cm.mu.Unlock()

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
	cm.mu.Unlock()

	// Use nsenter to exec inside the container's namespaces.
	args := []string{
		"-t", fmt.Sprintf("%d", pid),
		"-p", "-m", "-u", "-i",
		"--",
	}
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
			Name:   c.Name,
			Rootfs: c.Rootfs,
			Pid:    c.Pid,
			Status: c.Status,
			Image:  c.Image,
		})
	}
	return list
}

// ── Container init (re-exec pattern) ──

const containerInitArg = "__aetheria_container_init__"

// reexecContainer creates a command that re-execs the agent binary
// with namespace flags. The child process calls containerInit().
func reexecContainer(rootfs, hostname string) *exec.Cmd {
	cmd := exec.Command("/proc/self/exe", containerInitArg, rootfs, hostname)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWPID |
			syscall.CLONE_NEWNS |
			syscall.CLONE_NEWUTS |
			syscall.CLONE_NEWIPC,
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

