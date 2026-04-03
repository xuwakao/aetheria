//go:build linux

// oci.go — Minimal OCI registry client for pulling container images.
//
// Supports Docker Hub and any OCI Distribution-compliant registry.
// Implements anonymous token auth, manifest/manifest list resolution,
// and streaming blob download.
//
// References:
//   - OCI Distribution Spec: https://github.com/opencontainers/distribution-spec
//   - OCI Image Spec: https://github.com/opencontainers/image-spec
//   - Docker Hub auth: https://auth.docker.io/token

package main

import (
	"compress/gzip"
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

// ── OCI Reference Parsing ──

// OCIRef represents a parsed image reference: [registry/]repo[:tag|@digest]
type OCIRef struct {
	Registry string // e.g., "registry-1.docker.io"
	Repo     string // e.g., "library/alpine"
	Tag      string // e.g., "latest"
}

// ParseOCIRef parses an image reference string.
//
//	"alpine"              → registry-1.docker.io/library/alpine:latest
//	"nginx:1.25"          → registry-1.docker.io/library/nginx:1.25
//	"ghcr.io/owner/repo"  → ghcr.io/owner/repo:latest
func ParseOCIRef(ref string) OCIRef {
	r := OCIRef{
		Registry: "registry-1.docker.io",
		Tag:      "latest",
	}

	// Split tag.
	if i := strings.LastIndex(ref, ":"); i > 0 && !strings.Contains(ref[i:], "/") {
		r.Tag = ref[i+1:]
		ref = ref[:i]
	}

	// Split registry from repo.
	// A registry contains a dot or colon (port), or is "localhost".
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) == 2 && (strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":") || parts[0] == "localhost") {
		r.Registry = parts[0]
		r.Repo = parts[1]
	} else {
		r.Repo = ref
	}

	// Docker Hub official images: "alpine" → "library/alpine"
	if r.Registry == "registry-1.docker.io" && !strings.Contains(r.Repo, "/") {
		r.Repo = "library/" + r.Repo
	}

	return r
}

// String returns the canonical reference string.
func (r OCIRef) String() string {
	s := r.Repo
	if r.Registry != "registry-1.docker.io" {
		s = r.Registry + "/" + s
	}
	if r.Tag != "latest" {
		s += ":" + r.Tag
	}
	return s
}

// ShortName returns a filesystem-safe name for caching: "library-alpine" or "ghcr.io-owner-repo".
func (r OCIRef) ShortName() string {
	name := r.Repo
	if r.Registry != "registry-1.docker.io" {
		name = r.Registry + "/" + name
	}
	return strings.NewReplacer("/", "-", ":", "-").Replace(name)
}

// ── OCI Types ──

// OCIDescriptor describes a content-addressable blob.
type OCIDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	Platform  *struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
	} `json:"platform,omitempty"`
}

// OCIManifest is an OCI image manifest.
type OCIManifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	Config        OCIDescriptor   `json:"config"`
	Layers        []OCIDescriptor `json:"layers"`
}

// OCIManifestList is a multi-arch manifest index.
type OCIManifestList struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	Manifests     []OCIDescriptor `json:"manifests"`
}

// OCIImageConfig is the config blob of an OCI image.
type OCIImageConfig struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Config       struct {
		Env        []string `json:"Env"`
		Cmd        []string `json:"Cmd"`
		Entrypoint []string `json:"Entrypoint"`
		WorkingDir string   `json:"WorkingDir"`
	} `json:"config"`
}

// ── Registry Client ──

var ociHTTPClient = &http.Client{
	Timeout: 10 * time.Minute,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		// Follow redirects (registries use 307 for blob storage backends like S3).
		// Strip Authorization header on cross-domain redirects to avoid leaking tokens.
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			req.Header.Del("Authorization")
		}
		return nil
	},
}

// authTokenCache caches tokens per registry+repo to avoid re-auth per request.
var authTokenCache = map[string]string{}

// getAuthToken obtains a bearer token for the given registry and repo.
// Docker Hub: GET https://auth.docker.io/token?service=registry.docker.io&scope=repository:{repo}:pull
// Other registries: parse www-authenticate header from 401 response.
func getAuthToken(ref OCIRef) (string, error) {
	cacheKey := ref.Registry + "/" + ref.Repo
	if token, ok := authTokenCache[cacheKey]; ok {
		return token, nil
	}

	// Determine auth endpoint by probing the registry.
	baseURL := "https://" + ref.Registry
	resp, err := ociHTTPClient.Get(baseURL + "/v2/")
	if err != nil {
		return "", fmt.Errorf("probe registry %s: %w", ref.Registry, err)
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		// No auth needed (e.g., local registry).
		return "", nil
	}

	if resp.StatusCode != http.StatusUnauthorized {
		return "", fmt.Errorf("registry %s returned %d (expected 401)", ref.Registry, resp.StatusCode)
	}

	// Parse WWW-Authenticate header: Bearer realm="...",service="...",scope="..."
	wwwAuth := resp.Header.Get("WWW-Authenticate")
	realm, service := parseWWWAuthenticate(wwwAuth)
	if realm == "" {
		return "", fmt.Errorf("registry %s: no Bearer realm in WWW-Authenticate", ref.Registry)
	}

	// Fetch token.
	scope := fmt.Sprintf("repository:%s:pull", ref.Repo)
	tokenURL := fmt.Sprintf("%s?service=%s&scope=%s", realm, service, scope)
	resp, err = ociHTTPClient.Get(tokenURL)
	if err != nil {
		return "", fmt.Errorf("auth token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("auth token: HTTP %d", resp.StatusCode)
	}

	var tokenResp struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("parse auth token: %w", err)
	}

	token := tokenResp.Token
	if token == "" {
		token = tokenResp.AccessToken // Some registries use access_token
	}

	authTokenCache[cacheKey] = token
	return token, nil
}

// parseWWWAuthenticate extracts realm and service from a Bearer challenge.
func parseWWWAuthenticate(header string) (realm, service string) {
	header = strings.TrimPrefix(header, "Bearer ")
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "realm=") {
			realm = strings.Trim(strings.TrimPrefix(part, "realm="), "\"")
		} else if strings.HasPrefix(part, "service=") {
			service = strings.Trim(strings.TrimPrefix(part, "service="), "\"")
		}
	}
	return
}

// fetchManifest retrieves and resolves the image manifest.
// Handles manifest lists (multi-arch) by selecting linux/arm64.
func fetchManifest(ref OCIRef, token string) (*OCIManifest, error) {
	baseURL := "https://" + ref.Registry
	url := fmt.Sprintf("%s/v2/%s/manifests/%s", baseURL, ref.Repo, ref.Tag)

	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// Accept both manifest and manifest list.
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
	}, ", "))

	resp, err := ociHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("fetch manifest: HTTP %d: %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	contentType := resp.Header.Get("Content-Type")

	// Check if this is a manifest list (multi-arch index).
	if isManifestList(contentType) {
		var index OCIManifestList
		if err := json.Unmarshal(data, &index); err != nil {
			return nil, fmt.Errorf("parse manifest list: %w", err)
		}
		// Find linux/arm64 manifest.
		digest, err := selectPlatform(index, "linux", "arm64")
		if err != nil {
			return nil, err
		}
		// Fetch the platform-specific manifest by digest.
		return fetchManifestByDigest(ref, token, digest)
	}

	// Single-platform manifest.
	var manifest OCIManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &manifest, nil
}

// isManifestList checks if the content type indicates a manifest list/index.
func isManifestList(contentType string) bool {
	return strings.Contains(contentType, "manifest.list") || strings.Contains(contentType, "image.index")
}

// selectPlatform finds the digest for the desired OS/arch in a manifest list.
func selectPlatform(index OCIManifestList, os, arch string) (string, error) {
	for _, m := range index.Manifests {
		if m.Platform != nil && m.Platform.OS == os && m.Platform.Architecture == arch {
			return m.Digest, nil
		}
	}
	// List available platforms for error message.
	var available []string
	for _, m := range index.Manifests {
		if m.Platform != nil {
			available = append(available, m.Platform.OS+"/"+m.Platform.Architecture)
		}
	}
	return "", fmt.Errorf("no %s/%s manifest found (available: %s)", os, arch, strings.Join(available, ", "))
}

// fetchManifestByDigest fetches a manifest by its content digest.
func fetchManifestByDigest(ref OCIRef, token, digest string) (*OCIManifest, error) {
	baseURL := "https://" + ref.Registry
	url := fmt.Sprintf("%s/v2/%s/manifests/%s", baseURL, ref.Repo, digest)

	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")

	resp, err := ociHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest by digest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch manifest by digest: HTTP %d", resp.StatusCode)
	}

	var manifest OCIManifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &manifest, nil
}

// fetchBlob downloads a blob (layer or config) to a file.
// Follows redirects (registries redirect to CDN/S3 for actual content).
func fetchBlob(ref OCIRef, token, digest, destPath string) error {
	baseURL := "https://" + ref.Registry
	url := fmt.Sprintf("%s/v2/%s/blobs/%s", baseURL, ref.Repo, digest)

	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := ociHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch blob %s: %w", digest[:16], err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch blob %s: HTTP %d", digest[:16], resp.StatusCode)
	}

	// Write to temp file, then rename (atomic).
	tmpPath := destPath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmpPath, err)
	}

	written, err := io.Copy(f, resp.Body)
	f.Close()
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("download blob %s: %w", digest[:16], err)
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename blob: %w", err)
	}

	log.Printf("[oci] downloaded %s (%d bytes)", digest[:16], written)
	return nil
}

// ── OCI Pull Pipeline ──

// pullOCIImage pulls an OCI image from a registry and extracts it to a rootfs directory.
// Returns the rootfs path.
func pullOCIImage(ref string) (string, error) {
	parsed := ParseOCIRef(ref)
	log.Printf("[oci] pulling %s (registry=%s, repo=%s, tag=%s)", ref, parsed.Registry, parsed.Repo, parsed.Tag)

	// 1. Authenticate.
	token, err := getAuthToken(parsed)
	if err != nil {
		return "", fmt.Errorf("auth: %w", err)
	}

	// 2. Fetch manifest (resolves manifest list → platform-specific manifest).
	manifest, err := fetchManifest(parsed, token)
	if err != nil {
		return "", fmt.Errorf("manifest: %w", err)
	}
	log.Printf("[oci] manifest: %d layers, config=%s", len(manifest.Layers), manifest.Config.Digest[:16])

	// 3. Prepare cache directory.
	// Use short digest (first 12 chars of sha256) for cache dir name.
	digestShort := strings.TrimPrefix(manifest.Config.Digest, "sha256:")
	if len(digestShort) > 12 {
		digestShort = digestShort[:12]
	}
	// Store OCI rootfs on the storage backend (ext4), not virtiofs.
	// virtiofs/APFS lacks proper Linux symlink semantics.
	storageBase := filepath.Dir(containersDir) // e.g., /mnt/data or /var/aetheria
	cacheDir := filepath.Join(storageBase, "images", "oci", parsed.ShortName(), digestShort)
	rootfs := filepath.Join(cacheDir, "rootfs")

	// Skip if already fully extracted (sentinel file marks completion).
	completeMarker := filepath.Join(cacheDir, ".oci-complete")
	if _, err := os.Stat(completeMarker); err == nil {
		log.Printf("[oci] %s already cached at %s", ref, rootfs)
		return rootfs, nil
	}

	// Clean up any incomplete previous extraction.
	os.RemoveAll(rootfs)
	os.MkdirAll(cacheDir, 0755)
	os.MkdirAll(rootfs, 0755)

	// 4. Download and extract layers (sequentially, flattened into rootfs).
	for i, layer := range manifest.Layers {
		layerPath := filepath.Join(cacheDir, fmt.Sprintf("layer-%d.tar.gz", i))

		// Download layer if not cached.
		if _, err := os.Stat(layerPath); os.IsNotExist(err) {
			log.Printf("[oci] downloading layer %d/%d (%s, %d bytes)", i+1, len(manifest.Layers), layer.Digest[:16], layer.Size)
			if err := fetchBlob(parsed, token, layer.Digest, layerPath); err != nil {
				return "", fmt.Errorf("layer %d: %w", i, err)
			}
		}

		// Extract layer into rootfs (handles whiteouts).
		log.Printf("[oci] extracting layer %d/%d", i+1, len(manifest.Layers))
		if err := extractOCILayer(layerPath, rootfs); err != nil {
			return "", fmt.Errorf("extract layer %d: %w", i, err)
		}
	}

	// 5. Download config blob (for future use: Env, Cmd, Entrypoint).
	configPath := filepath.Join(cacheDir, "config.json")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := fetchBlob(parsed, token, manifest.Config.Digest, configPath); err != nil {
			log.Printf("[oci] warning: failed to download config: %v", err)
			// Non-fatal — rootfs is usable without config
		}
	}

	// 6. Set up DNS.
	os.MkdirAll(filepath.Join(rootfs, "etc"), 0755)
	os.WriteFile(filepath.Join(rootfs, "etc", "resolv.conf"),
		[]byte("nameserver 8.8.8.8\nnameserver 1.1.1.1\n"), 0644)

	// Mark extraction as complete (sentinel for cache validity).
	os.WriteFile(completeMarker, []byte(ref+"\n"), 0644)

	log.Printf("[oci] pulled %s → %s", ref, rootfs)
	return rootfs, nil
}

// extractOCILayer extracts a gzipped tar layer into the target directory,
// handling OCI whiteout files (.wh. prefix) and opaque directories (.wh..wh..opq).
func extractOCILayer(layerPath, target string) error {
	f, err := os.Open(layerPath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		// Not gzipped — try plain tar (some layers use uncompressed tar).
		f.Seek(0, io.SeekStart)
		return extractTar(f, target)
	}
	defer gz.Close()

	return extractTar(gz, target)
}

// extractTar extracts a tar stream into target, processing OCI whiteouts.
// Strategy: extract with tar to temp dir (handles hardlinks, xattrs, permissions),
// then scan for .wh. whiteout markers and merge into target.
func extractTar(r io.Reader, target string) error {
	tmpDir := target + ".layer-tmp"
	os.RemoveAll(tmpDir)
	os.MkdirAll(tmpDir, 0755)
	defer os.RemoveAll(tmpDir)

	// Extract layer to temp dir.
	cmd := newTarCmd(r, tmpDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Exit code 2 = non-fatal warnings (symlink metadata on virtiofs)
		if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 2 {
			return fmt.Errorf("tar extract: %v: %s", err, string(out))
		}
	}

	// Process whiteouts and merge into target.
	return mergeLayerWithWhiteouts(tmpDir, target)
}

// newTarCmd creates a tar extraction command reading from stdin.
func newTarCmd(r io.Reader, dest string) *exec.Cmd {
	cmd := exec.Command("tar", "xf", "-", "-C", dest)
	cmd.Stdin = r
	return cmd
}

// mergeLayerWithWhiteouts merges a layer directory into the target,
// handling OCI whiteout markers.
func mergeLayerWithWhiteouts(layerDir, target string) error {
	return filepath.Walk(layerDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, _ := filepath.Rel(layerDir, path)
		if relPath == "." {
			return nil
		}

		destPath := filepath.Join(target, relPath)
		name := filepath.Base(relPath)

		// Handle opaque whiteout: marks directory as replaced (delete all existing contents).
		if name == ".wh..wh..opq" {
			dir := filepath.Dir(destPath)
			// Remove existing directory contents (but keep the directory itself).
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				os.RemoveAll(filepath.Join(dir, e.Name()))
			}
			return nil
		}

		// Handle file whiteout: .wh.<filename> means delete <filename>.
		if strings.HasPrefix(name, ".wh.") {
			deleteName := strings.TrimPrefix(name, ".wh.")
			deleteTarget := filepath.Join(filepath.Dir(destPath), deleteName)
			os.RemoveAll(deleteTarget)
			return nil
		}

		// Normal file/dir — copy to target.
		if info.IsDir() {
			return os.MkdirAll(destPath, info.Mode())
		}

		// For symlinks, recreate the link.
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			os.Remove(destPath) // remove existing if any
			return os.Symlink(linkTarget, destPath)
		}

		// Regular file: copy with ownership.
		return copyFile(path, destPath, info)
	})
}

// copyFile copies a file preserving permissions and ownership.
func copyFile(src, dst string, srcInfo os.FileInfo) error {
	os.MkdirAll(filepath.Dir(dst), 0755)

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return err
	}

	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}

	// Preserve uid/gid (important for OCI images with non-root files).
	if stat, ok := srcInfo.Sys().(*syscall.Stat_t); ok {
		os.Chown(dst, int(stat.Uid), int(stat.Gid))
	}
	return nil
}

