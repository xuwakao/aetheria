//go:build linux

// cgroup.go — Cgroups v2 resource isolation for containers.
//
// Creates a per-container cgroup at /sys/fs/cgroup/aetheria/<name>/,
// sets CPU, memory, and PID limits, and moves the container PID into it.
// Cleanup removes the cgroup directory on container stop.

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const cgroupBase = "/sys/fs/cgroup/aetheria"

// initCgroupHierarchy prepares the cgroup v2 hierarchy for container use.
// Cgroups v2 "no internal processes" rule: a cgroup cannot both enable
// subtree controllers AND have member processes. On a fresh boot, all
// processes (including PID 1) are in the root cgroup, blocking controller
// enablement. Fix: move all root cgroup processes into a leaf cgroup first.
// Called once at agent startup.
func initCgroupHierarchy() {
	// Create init.scope for system processes.
	initScope := "/sys/fs/cgroup/init.scope"
	os.MkdirAll(initScope, 0755)

	// Move all processes from root cgroup to init.scope.
	data, err := os.ReadFile("/sys/fs/cgroup/cgroup.procs")
	if err != nil {
		log.Printf("[cgroup] read root procs: %v", err)
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		// Writing a PID to cgroup.procs moves it. Errors are expected
		// for kernel threads (cannot be moved) — ignore them.
		os.WriteFile(filepath.Join(initScope, "cgroup.procs"), []byte(line), 0644)
	}

	// Now enable controllers at root level.
	enableControllers("/sys/fs/cgroup")

	// Create aetheria parent cgroup and enable controllers there too.
	os.MkdirAll(cgroupBase, 0755)
	enableControllers(cgroupBase)

	log.Printf("[cgroup] hierarchy initialized: init.scope + %s", cgroupBase)
}

// ResourceLimits defines container resource constraints via cgroups v2.
type ResourceLimits struct {
	// MemoryMax in bytes. 0 = unlimited.
	// Written to memory.max (e.g., "536870912" for 512MB).
	MemoryMax int64 `json:"memory_max,omitempty"`

	// CPUMax as a fraction of CPUs (e.g., 0.5 = half a core, 2.0 = two cores).
	// 0 = unlimited. Converted to cpu.max as "quota period" microseconds.
	// Period is fixed at 100000us (100ms). Quota = CPUMax * 100000.
	CPUMax float64 `json:"cpu_max,omitempty"`

	// PidsMax limits the number of processes. 0 = unlimited.
	// Written to pids.max.
	PidsMax int64 `json:"pids_max,omitempty"`
}

// IsEmpty returns true if no limits are set.
func (r ResourceLimits) IsEmpty() bool {
	return r.MemoryMax == 0 && r.CPUMax == 0 && r.PidsMax == 0
}

// setupCgroup creates a cgroup for the container and applies resource limits.
// Must be called after the container process starts (need PID).
func setupCgroup(containerName string, pid int, limits ResourceLimits) error {
	cgroupPath := filepath.Join(cgroupBase, containerName)

	if err := os.MkdirAll(cgroupPath, 0755); err != nil {
		return fmt.Errorf("create cgroup dir %s: %w", cgroupPath, err)
	}

	// Apply limits.
	if limits.MemoryMax > 0 {
		if err := writeCgroupFile(cgroupPath, "memory.max", strconv.FormatInt(limits.MemoryMax, 10)); err != nil {
			return fmt.Errorf("set memory.max: %w", err)
		}
		log.Printf("[cgroup] %s: memory.max=%d bytes", containerName, limits.MemoryMax)
	}

	if limits.CPUMax > 0 {
		const period = 100000 // 100ms in microseconds
		quota := int64(limits.CPUMax * float64(period))
		if quota < 1000 {
			quota = 1000 // minimum 1ms quota
		}
		val := fmt.Sprintf("%d %d", quota, period)
		if err := writeCgroupFile(cgroupPath, "cpu.max", val); err != nil {
			return fmt.Errorf("set cpu.max: %w", err)
		}
		log.Printf("[cgroup] %s: cpu.max=%s", containerName, val)
	}

	if limits.PidsMax > 0 {
		if err := writeCgroupFile(cgroupPath, "pids.max", strconv.FormatInt(limits.PidsMax, 10)); err != nil {
			return fmt.Errorf("set pids.max: %w", err)
		}
		log.Printf("[cgroup] %s: pids.max=%d", containerName, limits.PidsMax)
	}

	// Move the container PID into the cgroup.
	if err := writeCgroupFile(cgroupPath, "cgroup.procs", strconv.Itoa(pid)); err != nil {
		return fmt.Errorf("move pid %d to cgroup: %w", pid, err)
	}

	log.Printf("[cgroup] %s: pid %d moved to %s", containerName, pid, cgroupPath)
	return nil
}

// cleanupCgroup removes the container's cgroup directory.
// All processes must have exited before this is called.
func cleanupCgroup(containerName string) {
	cgroupPath := filepath.Join(cgroupBase, containerName)

	// Read cgroup.procs to verify it's empty.
	data, err := os.ReadFile(filepath.Join(cgroupPath, "cgroup.procs"))
	if err == nil && len(strings.TrimSpace(string(data))) > 0 {
		log.Printf("[cgroup] warning: %s still has processes, skipping cleanup", containerName)
		return
	}

	if err := os.Remove(cgroupPath); err != nil && !os.IsNotExist(err) {
		log.Printf("[cgroup] cleanup %s: %v", cgroupPath, err)
	} else {
		log.Printf("[cgroup] %s: cgroup removed", containerName)
	}
}

// enableControllers writes +cpu +memory +pids to cgroup.subtree_control.
// This enables the controllers for child cgroups.
func enableControllers(parentPath string) {
	subtreeControl := filepath.Join(parentPath, "cgroup.subtree_control")
	for _, ctrl := range []string{"+cpu", "+memory", "+pids"} {
		if err := os.WriteFile(subtreeControl, []byte(ctrl), 0644); err != nil {
			log.Printf("[cgroup] enable %s at %s: %v (may already be enabled)", ctrl, parentPath, err)
		}
	}
}

// writeCgroupFile writes a value to a cgroup control file.
func writeCgroupFile(cgroupPath, filename, value string) error {
	path := filepath.Join(cgroupPath, filename)
	return os.WriteFile(path, []byte(value), 0644)
}
