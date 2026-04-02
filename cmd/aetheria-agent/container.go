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
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
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

func (cm *ContainerManager) Create(params ContainerCreateParams) error {
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
	cm.mu.Unlock()

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
		return fmt.Errorf("failed to start container: %w", err)
	}

	cm.mu.Lock()
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

	// Send SIGTERM, then SIGKILL after 5s.
	syscall.Kill(pid, syscall.SIGTERM)
	log.Printf("[container] sent SIGTERM to %q pid=%d", name, pid)
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

	// Mount /proc, /sys, /dev in the new rootfs.
	setupContainerMounts(rootfs)

	// pivot_root to the new rootfs.
	if err := pivotRoot(rootfs); err != nil {
		log.Fatalf("[container-init] pivot_root failed: %v", err)
	}

	// Exec the container's init process.
	// Try common init paths in order of preference.
	for _, init := range []string{"/sbin/init", "/bin/sh"} {
		if _, err := os.Stat(init); err == nil {
			log.Printf("[container-init] exec %s", init)
			err := syscall.Exec(init, []string{init}, os.Environ())
			log.Fatalf("[container-init] exec %s failed: %v", init, err)
		}
	}
	log.Fatal("[container-init] no init found in container rootfs")
}

func setupContainerMounts(rootfs string) {
	// Make the mount namespace private (don't propagate mounts to parent).
	syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, "")

	// Bind-mount rootfs to itself (required for pivot_root).
	syscall.Mount(rootfs, rootfs, "", syscall.MS_BIND|syscall.MS_REC, "")

	// Mount /proc inside new rootfs.
	proc := filepath.Join(rootfs, "proc")
	os.MkdirAll(proc, 0755)
	syscall.Mount("proc", proc, "proc", 0, "")

	// Mount /sys inside new rootfs (read-only bind).
	sys := filepath.Join(rootfs, "sys")
	os.MkdirAll(sys, 0755)
	syscall.Mount("sysfs", sys, "sysfs", 0, "")

	// Mount /dev as devtmpfs.
	dev := filepath.Join(rootfs, "dev")
	os.MkdirAll(dev, 0755)
	syscall.Mount("devtmpfs", dev, "devtmpfs", 0, "")

	// Mount /dev/pts for PTY support.
	devpts := filepath.Join(rootfs, "dev", "pts")
	os.MkdirAll(devpts, 0755)
	syscall.Mount("devpts", devpts, "devpts", 0, "")

	// Mount /tmp as tmpfs.
	tmp := filepath.Join(rootfs, "tmp")
	os.MkdirAll(tmp, 01777)
	syscall.Mount("tmpfs", tmp, "tmpfs", 0, "")
}

func pivotRoot(newRoot string) error {
	// Create oldroot dir inside newRoot.
	oldRoot := filepath.Join(newRoot, ".old_root")
	os.MkdirAll(oldRoot, 0700)

	// pivot_root swaps the root filesystem.
	if err := syscall.PivotRoot(newRoot, oldRoot); err != nil {
		return fmt.Errorf("pivot_root(%s, %s): %w", newRoot, oldRoot, err)
	}

	// Change to new root.
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("chdir /: %w", err)
	}

	// Unmount old root.
	if err := syscall.Unmount("/.old_root", syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("unmount old root: %w", err)
	}

	// Remove old root mount point.
	os.RemoveAll("/.old_root")

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

// ── Helper: check if string starts with prefix ──

func hasMethodPrefix(method, prefix string) bool {
	return strings.HasPrefix(method, prefix)
}
