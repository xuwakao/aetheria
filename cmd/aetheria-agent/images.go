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
		URL:     "https://cdimage.ubuntu.com/ubuntu-base/releases/24.04/release/ubuntu-base-24.04.2-base-arm64.tar.gz",
	},
	"debian": {
		Name:    "debian",
		Version: "12",
		Arch:    "arm64",
		// Debian doesn't have official minirootfs. Use debootstrap-generated tarball.
		// For now, use a cloud image base from a mirror.
		URL: "https://github.com/debuerreotype/docker-debian-artifacts/raw/dist-arm64v8/bookworm/rootfs.tar.xz",
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

// imageCachePath returns the path to the cached tarball.
func imageCachePath(name string) string {
	return filepath.Join(imagesDir, name+".tar.gz")
}

// imageExtractPath returns the path for the extracted base rootfs.
func imageExtractPath(name string) string {
	return filepath.Join(imagesDir, name, "rootfs")
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
	cmd := exec.Command("tar", "xf", tarball, "-C", dest)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("extract %s: %v: %s", name, err, string(out))
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
		name := strings.TrimSuffix(e.Name(), ".tar.gz")
		if info, ok := imageRegistry[name]; ok {
			fi, _ := e.Info()
			info.Size = fi.Size()
			result.Cached = append(result.Cached, info)
		}
	}

	return result
}

// PrepareContainerRootfs creates a container's rootfs from a base image.
// For now: copies the base image rootfs. Future: overlay mount.
func PrepareContainerRootfs(containerName, imageName string) (string, error) {
	baseRootfs := imageExtractPath(imageName)
	if _, err := os.Stat(filepath.Join(baseRootfs, "bin")); os.IsNotExist(err) {
		// Image not extracted — try to pull it.
		if err := PullImage(imageName); err != nil {
			return "", fmt.Errorf("pull image %s: %w", imageName, err)
		}
	}

	containerRootfs := filepath.Join(containersDir, containerName, "rootfs")
	os.MkdirAll(containerRootfs, 0755)

	// Check if already prepared.
	if _, err := os.Stat(filepath.Join(containerRootfs, "bin")); err == nil {
		return containerRootfs, nil
	}

	// Copy base rootfs to container directory.
	// Future: use overlayfs (lower=base, upper=container delta) for CoW.
	log.Printf("[image] copying %s rootfs to %s", imageName, containerRootfs)
	cmd := exec.Command("cp", "-a", baseRootfs+"/.", containerRootfs+"/")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("copy rootfs: %v: %s", err, string(out))
	}

	// Set up basic resolv.conf.
	os.WriteFile(filepath.Join(containerRootfs, "etc", "resolv.conf"),
		[]byte("nameserver 8.8.8.8\nnameserver 1.1.1.1\n"), 0644)

	return containerRootfs, nil
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
