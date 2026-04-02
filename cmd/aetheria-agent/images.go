//go:build linux

// images.go — Distro rootfs image management.
//
// Downloads, caches, and extracts Linux distribution root filesystem images.
// Supports Alpine, Ubuntu, Debian, Fedora. Images are stored as tarballs
// in /var/aetheria/images/ and extracted per-container.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ImageInfo describes a downloadable distro rootfs.
type ImageInfo struct {
	Name    string `json:"name"`    // e.g., "alpine", "ubuntu"
	Version string `json:"version"` // e.g., "3.21", "22.04"
	Arch    string `json:"arch"`    // e.g., "aarch64", "x86_64"
	URL     string `json:"url"`     // download URL
	Size    int64  `json:"size"`    // cached file size (0 if not downloaded)
}

// imageRegistry maps distro names to download URLs.
// ARM64 variants for Apple Silicon VMs.
var imageRegistry = map[string]ImageInfo{
	"alpine": {
		Name:    "alpine",
		Version: "3.21",
		Arch:    "aarch64",
		URL:     "https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/aarch64/alpine-minirootfs-3.21.3-aarch64.tar.gz",
	},
	"ubuntu": {
		Name:    "ubuntu",
		Version: "24.04",
		Arch:    "arm64",
		URL:     "https://cdimage.ubuntu.com/ubuntu-base/releases/24.04/release/ubuntu-base-24.04.4-base-arm64.tar.gz",
	},
	"debian": {
		Name:    "debian",
		Version: "12",
		Arch:    "arm64",
		// Official Debian cloud image from debian.org CDN.
		URL: "https://cloud.debian.org/images/cloud/bookworm/latest/rootfs/debian-12-nocloud-arm64-rootfs.tar.xz",
	},
}

// ── RPC types ──

type ImagePullParams struct {
	Name string `json:"name"` // distro name: "alpine", "ubuntu", "debian"
}

type ImageListResult struct {
	Available []ImageInfo `json:"available"` // registered images
	Cached    []ImageInfo `json:"cached"`    // downloaded images
}

// ── Image Manager ──

// imageCachePath returns the path to the cached tarball, preserving
// the original extension from the URL (e.g., .tar.gz or .tar.xz).
func imageCachePath(name string) string {
	info, ok := imageRegistry[name]
	if !ok {
		return filepath.Join(imagesDir, name+".tar.gz")
	}
	ext := ".tar.gz"
	if strings.HasSuffix(info.URL, ".tar.xz") {
		ext = ".tar.xz"
	}
	return filepath.Join(imagesDir, name+ext)
}

// imageExtractPath returns the path for the extracted base rootfs.
// Extract to tmpfs (backed by VM RAM, not the 256MB ext4 rootfs which
// is too small for Ubuntu ~70MB). NOT virtiofs because macOS APFS
// symlinks are incompatible with Linux symlinks.
func imageExtractPath(name string) string {
	return filepath.Join("/tmp/aetheria/images", name, "rootfs")
}

// PullImage downloads a distro rootfs tarball if not cached.
func PullImage(name string) error {
	info, ok := imageRegistry[name]
	if !ok {
		avail := make([]string, 0, len(imageRegistry))
		for k := range imageRegistry {
			avail = append(avail, k)
		}
		return fmt.Errorf("unknown image %q (available: %s)", name, strings.Join(avail, ", "))
	}

	cachePath := imageCachePath(name)

	// Check if already cached.
	if st, err := os.Stat(cachePath); err == nil && st.Size() > 0 {
		log.Printf("[image] %s already cached (%d bytes)", name, st.Size())
		return extractImage(name, cachePath)
	}

	log.Printf("[image] downloading %s from %s", name, info.URL)

	// Download with timeout.
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(info.URL)
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", name, resp.StatusCode)
	}

	// Write to temp file, then rename (atomic).
	tmpPath := cachePath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmpPath, err)
	}

	written, err := io.Copy(f, resp.Body)
	f.Close()
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("download %s: write failed: %w", name, err)
	}

	if err := os.Rename(tmpPath, cachePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename %s: %w", name, err)
	}

	log.Printf("[image] downloaded %s (%d bytes)", name, written)
	return extractImage(name, cachePath)
}

// extractImage extracts a cached tarball to the image rootfs directory.
func extractImage(name, tarball string) error {
	dest := imageExtractPath(name)

	// Skip if already extracted.
	if _, err := os.Stat(filepath.Join(dest, "bin")); err == nil {
		log.Printf("[image] %s already extracted at %s", name, dest)
		return nil
	}

	os.MkdirAll(dest, 0755)
	log.Printf("[image] extracting %s to %s", tarball, dest)

	// Use tar command (handles both .tar.gz and .tar.xz).
	// On virtiofs, symlink chown/utime fails (I/O error) because the passthrough
	// uses a placeholder fd for symlinks on macOS. These errors are non-fatal —
	// the files are extracted correctly, only metadata on symlinks is wrong.
	// Accept exit code 0 (success) or 2 (non-fatal warnings).
	cmd := exec.Command("tar", "xf", tarball, "-C", dest)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
			log.Printf("[image] tar warnings (non-fatal) for %s", name)
		} else {
			return fmt.Errorf("extract %s: %v: %s", name, err, string(out))
		}
	}

	log.Printf("[image] extracted %s", name)
	return nil
}

// ListImages returns available and cached images.
func ListImages() ImageListResult {
	result := ImageListResult{}

	for _, info := range imageRegistry {
		result.Available = append(result.Available, info)
	}

	entries, _ := os.ReadDir(imagesDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fname := e.Name()
		name := strings.TrimSuffix(strings.TrimSuffix(fname, ".tar.gz"), ".tar.xz")
		if info, ok := imageRegistry[name]; ok {
			fi, _ := e.Info()
			info.Size = fi.Size()
			result.Cached = append(result.Cached, info)
		}
	}

	return result
}

// PrepareContainerRootfs creates a container's rootfs from a base image
// using overlayfs (CoW). The base image is the read-only lower layer;
// per-container changes go to the upper layer.
func PrepareContainerRootfs(containerName, imageName string) (string, error) {
	baseRootfs := imageExtractPath(imageName)
	if _, err := os.Stat(filepath.Join(baseRootfs, "bin")); os.IsNotExist(err) {
		if err := PullImage(imageName); err != nil {
			return "", fmt.Errorf("pull image %s: %w", imageName, err)
		}
	}

	containerDir := filepath.Join(containersDir, containerName)
	merged := filepath.Join(containerDir, "rootfs")
	upper := filepath.Join(containerDir, "upper")
	work := filepath.Join(containerDir, "work")

	// Check if already mounted.
	if _, err := os.Stat(filepath.Join(merged, "bin")); err == nil {
		return merged, nil
	}

	for _, d := range []string{merged, upper, work} {
		os.MkdirAll(d, 0755)
	}

	// Try overlayfs mount (requires CONFIG_OVERLAY_FS=y, confirmed in kernel).
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", baseRootfs, upper, work)
	err := syscall.Mount("overlay", merged, "overlay", 0, opts)
	if err != nil {
		// Fallback to cp -a if overlayfs not available.
		log.Printf("[image] overlayfs failed (%v), falling back to cp -a", err)
		cmd := exec.Command("cp", "-a", baseRootfs+"/.", merged+"/")
		if out, cpErr := cmd.CombinedOutput(); cpErr != nil {
			return "", fmt.Errorf("copy rootfs: %v: %s", cpErr, string(out))
		}
	} else {
		log.Printf("[image] overlayfs mounted: lower=%s upper=%s", baseRootfs, upper)
	}

	// Set up DNS.
	os.MkdirAll(filepath.Join(merged, "etc"), 0755)
	os.WriteFile(filepath.Join(merged, "etc", "resolv.conf"),
		[]byte("nameserver 8.8.8.8\nnameserver 1.1.1.1\n"), 0644)

	return merged, nil
}

// ── RPC handlers ──

func handleImageRPC(req Request) Response {
	switch req.Method {
	case "image.pull":
		var params ImagePullParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return Response{Error: "invalid params: " + err.Error(), ID: req.ID}
		}
		if err := PullImage(params.Name); err != nil {
			return Response{Error: err.Error(), ID: req.ID}
		}
		return Response{Result: "pulled", ID: req.ID}

	case "image.list":
		return Response{Result: ListImages(), ID: req.ID}

	default:
		return Response{Error: fmt.Sprintf("unknown image method: %s", req.Method), ID: req.ID}
	}
}
