// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package spec provides the generic specification and installer framework for managed tools.
package spec

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/fetch"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/github"
	toolversions "github.com/gke-labs/gke-labs-infra/ap/pkg/tool/versions"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"
)

// Archive defines the packaging layout and extraction of a tool distribution.
type Archive interface {
	Extract(archivePath, binary, dest string) error
}

// Bare represents an uncompressed binary release asset.
type Bare struct{}

// Extract copies the uncompressed binary from archivePath to dest.
func (Bare) Extract(archivePath, binary, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", filepath.Dir(dest), err)
	}
	if err := fileutils.CopyFile(archivePath, dest, 0755); err != nil {
		return fmt.Errorf("failed to install binary %s: %w", dest, err)
	}
	return nil
}

// TarGz represents a .tar.gz archive containing the binary.
type TarGz struct{}

// Extract extracts the binary from a .tar.gz archive to dest.
func (TarGz) Extract(archivePath, binary, dest string) error {
	return extractTarGz(archivePath, binary, dest)
}

func extractTarGz(archivePath, binary, dest string) error {
	archiveFile, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("failed to open archive %s: %w", archivePath, err)
	}
	defer func() {
		if closeErr := archiveFile.Close(); closeErr != nil {
			klog.V(2).Infof("failed to close %s: %v", archivePath, closeErr)
		}
	}()

	gzipReader, err := gzip.NewReader(archiveFile)
	if err != nil {
		return fmt.Errorf("failed to initialize gzip reader for %s: %w", archivePath, err)
	}
	defer func() {
		if closeErr := gzipReader.Close(); closeErr != nil {
			klog.V(2).Infof("failed to close gzip reader: %v", closeErr)
		}
	}()

	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed reading tar archive %s: %w", archivePath, err)
		}
		cleanName := filepath.Clean(header.Name)
		if (header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA) && (cleanName == binary || filepath.Base(cleanName) == binary) {
			return extractStreamToFile(tarReader, dest)
		}
	}
	return fmt.Errorf("target binary %q not found in archive %s", binary, archivePath)
}

func extractStreamToFile(r io.Reader, dest string) error {
	tempFile, err := fileutils.CreateTempFile(dest)
	if err != nil {
		return err
	}
	defer tempFile.Cleanup()

	if _, err := io.Copy(tempFile, r); err != nil {
		return err
	}
	return tempFile.Rename(0755)
}

// Source provides versions, asset URLs, and digests for a tool.
type Source interface {
	Versions(ctx context.Context) ([]string, error)
	AssetURL(tag, goos, goarch string) string
	Digest(ctx context.Context, tag, goos, goarch string) (string, error)
}

// GitHubReleases fetches versions from GitHub releases and computes digests via fetch.Digest.
type GitHubReleases struct {
	Repo       string
	TagPrefix  string // optional prefix to filter and strip from release tags (e.g. "kustomize/")
	Asset      func(tag, goos, goarch string) string
	APIBaseURL string // optional override for api.github.com
	BaseURL    string // optional override for release asset downloads
}

// Versions returns release tags from GitHub API, newest first.
func (g *GitHubReleases) Versions(ctx context.Context) ([]string, error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, github.APITimeout)
		defer cancel()
	}

	apiBase := g.APIBaseURL
	if apiBase == "" {
		apiBase = os.Getenv("GITHUB_API_BASE_URL")
	}
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}

	rawReleases, err := github.ListReleases(ctx, apiBase, g.Repo, 100)
	if err != nil {
		return nil, fmt.Errorf("failed to list %s releases: %w", g.Repo, err)
	}

	var versions []string
	seen := make(map[string]bool)
	for _, release := range rawReleases {
		if release.Draft || release.Prerelease {
			continue
		}
		rawTag := release.TagName
		if g.TagPrefix != "" {
			if !strings.HasPrefix(rawTag, g.TagPrefix) {
				continue
			}
			rawTag = strings.TrimPrefix(rawTag, g.TagPrefix)
		}
		version := strings.TrimPrefix(rawTag, "v")
		if version == "" || version[0] < '0' || version[0] > '9' || strings.Contains(version, "-") {
			continue
		}
		normalizedTag := NormalizeTag(rawTag)
		if seen[normalizedTag] {
			continue
		}
		seen[normalizedTag] = true
		versions = append(versions, normalizedTag)
	}

	sort.Slice(versions, func(i, j int) bool {
		return toolversions.Compare(versions[i], versions[j]) > 0
	})

	return versions, nil
}

// AssetURL returns the download URL for the asset on GitHub releases.
func (g *GitHubReleases) AssetURL(tag, goos, goarch string) string {
	cleanTag := tag
	if g.TagPrefix != "" {
		cleanTag = strings.TrimPrefix(cleanTag, g.TagPrefix)
	}
	normalizedTag := NormalizeTag(cleanTag)
	asset := g.Asset(normalizedTag, goos, goarch)
	if g.BaseURL != "" {
		return fmt.Sprintf("%s/%s", strings.TrimRight(g.BaseURL, "/"), asset)
	}
	releaseTag := g.TagPrefix + normalizedTag
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", g.Repo, releaseTag, asset)
}

// Digest downloads the asset and computes its SHA-256 digest using fetch.Digest.
func (g *GitHubReleases) Digest(ctx context.Context, tag, goos, goarch string) (string, error) {
	url := g.AssetURL(tag, goos, goarch)
	return fetch.Digest(ctx, url)
}

// KubernetesRelease fetches versions, assets, and digests from dl.k8s.io.
type KubernetesRelease struct {
	BaseURL string // optional override for https://dl.k8s.io/release
}

func (k *KubernetesRelease) baseURL() string {
	if k.BaseURL != "" {
		return strings.TrimRight(k.BaseURL, "/")
	}
	return "https://dl.k8s.io/release"
}

// Versions reads the stable tag from https://dl.k8s.io/release/stable.txt (one entry).
func (k *KubernetesRelease) Versions(ctx context.Context) ([]string, error) {
	url := fmt.Sprintf("%s/stable.txt", k.baseURL())
	text, err := fetchText(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch stable version from %s: %w", url, err)
	}
	tag := NormalizeTag(text)
	if tag == "" || tag == "v" {
		return nil, fmt.Errorf("empty version from %s", url)
	}
	return []string{tag}, nil
}

// AssetURL returns the download URL for the binary on dl.k8s.io.
func (k *KubernetesRelease) AssetURL(tag, goos, goarch string) string {
	bin := "kubectl"
	if goos == "windows" {
		bin = "kubectl.exe"
	}
	normalizedTag := NormalizeTag(tag)
	return fmt.Sprintf("%s/%s/bin/%s/%s/%s", k.baseURL(), normalizedTag, goos, goarch, bin)
}

// Digest fetches the .sha256 sidecar next to the asset and trims it.
func (k *KubernetesRelease) Digest(ctx context.Context, tag, goos, goarch string) (string, error) {
	shaURL := k.AssetURL(tag, goos, goarch) + ".sha256"
	shaText, err := fetchText(ctx, shaURL)
	if err != nil {
		return "", fmt.Errorf("failed to fetch checksum from %s: %w", shaURL, err)
	}
	return ParseSidecarDigest(shaText)
}

// HelmRelease fetches versions from GitHub releases (helm/helm) and assets/digests from get.helm.sh.
type HelmRelease struct {
	BaseURL string // optional override for https://get.helm.sh
}

func (h *HelmRelease) baseURL() string {
	if h.BaseURL != "" {
		return strings.TrimRight(h.BaseURL, "/")
	}
	return "https://get.helm.sh"
}

// Versions returns release tags from GitHub API, newest first.
func (h *HelmRelease) Versions(ctx context.Context) ([]string, error) {
	return (&GitHubReleases{Repo: "helm/helm"}).Versions(ctx)
}

// AssetURL returns the download URL for the asset on get.helm.sh.
func (h *HelmRelease) AssetURL(tag, goos, goarch string) string {
	normalizedTag := NormalizeTag(tag)
	return fmt.Sprintf("%s/helm-%s-%s-%s.tar.gz", h.baseURL(), normalizedTag, goos, goarch)
}

// Digest fetches the .sha256 sidecar next to the asset and trims it.
func (h *HelmRelease) Digest(ctx context.Context, tag, goos, goarch string) (string, error) {
	shaURL := h.AssetURL(tag, goos, goarch) + ".sha256"
	shaText, err := fetchText(ctx, shaURL)
	if err != nil {
		return "", fmt.Errorf("failed to fetch checksum from %s: %w", shaURL, err)
	}
	return ParseSidecarDigest(shaText)
}

// ParseSidecarDigest extracts and formats a sha256 digest from a sidecar file's text content.
func ParseSidecarDigest(text string) (string, error) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", fmt.Errorf("empty checksum sidecar content")
	}
	digest := fields[0]
	if !strings.HasPrefix(digest, "sha256:") {
		digest = "sha256:" + digest
	}
	return digest, nil
}

const (
	fetchTextTimeout     = 30 * time.Second
	fetchTextMaxAttempts = 3
	fetchTextRetryPause  = 100 * time.Millisecond
)

// fetchTextAttempt executes a single HTTP GET attempt under a per-attempt deadline.
// It reports whether an error is retryable (transport error, attempt timeout, HTTP 5xx/429).
func fetchTextAttempt(ctx context.Context, url string) (text string, retryable bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTextTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", false, fmt.Errorf("failed to create request: %w", err)
	}
	request.Header.Set("User-Agent", "gke-labs-ap")

	response, err := scoped.HTTPClient(ctx).Do(request)
	if err != nil {
		return "", true, err
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			klog.V(2).Infof("failed to close response body: %v", closeErr)
		}
	}()

	if response.StatusCode == http.StatusOK {
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return "", true, fmt.Errorf("failed to read response: %w", err)
		}
		return strings.TrimSpace(string(body)), false, nil
	}

	if response.StatusCode == http.StatusTooManyRequests || (response.StatusCode >= 500 && response.StatusCode <= 599) {
		return "", true, fmt.Errorf("HTTP %s", response.Status)
	}

	return "", false, fmt.Errorf("HTTP %s", response.Status)
}

// fetchText fetches small text files (such as version feeds or checksum sidecars)
// and returns the trimmed content. It must not be used for large downloads.
// Each attempt runs under its own short deadline derived from ctx, and retries
// transient failures (transport errors, timeouts, and HTTP 5xx/429).
func fetchText(ctx context.Context, url string) (string, error) {
	var lastError error
	pause := fetchTextRetryPause
	for attempt := 0; attempt < fetchTextMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("failed to fetch from %s: %w", url, err)
		}

		text, retryable, err := fetchTextAttempt(ctx, url)
		if err == nil {
			return text, nil
		}
		if !retryable {
			return "", fmt.Errorf("failed to fetch from %s: %w", url, err)
		}
		lastError = err

		if attempt < fetchTextMaxAttempts-1 {
			select {
			case <-ctx.Done():
				return "", fmt.Errorf("failed to fetch from %s: %w", url, ctx.Err())
			case <-time.After(pause):
				pause *= 2
			}
		}
	}

	return "", fmt.Errorf("failed to fetch from %s: %w", url, lastError)
}

// Spec defines the configuration and behavior of a managed external tool.
type Spec struct {
	Name         string   // cache dir, config file .ap/<Name>.yaml and key, versions.yaml section, registry name
	Binary       string   // file name under bin/; defaults to Name
	Platforms    []string // "darwin-arm64", "linux-amd64", ...
	Source       Source   // where versions and assets come from
	Archive      Archive  // Bare{}
	VersionsYAML []byte   // embedded versions.yaml
	VersionsPath string   // path relative to repo root; defaults to ap/pkg/tool/<Name>/versions.yaml
}

// BinaryName returns the executable name for the current platform (e.g. "kind" or "kind.exe").
func (s *Spec) BinaryName() string {
	bin := s.Binary
	if bin == "" {
		bin = s.Name
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(bin, ".exe") {
		bin += ".exe"
	}
	return bin
}

// DownloadURL returns the download URL for tag on the given os/arch.
func (s *Spec) DownloadURL(tag, goos, goarch string) string {
	return s.Source.AssetURL(tag, goos, goarch)
}

var (
	releasesCacheMu sync.Mutex
	releasesCache   = make(map[string][]toolversions.Release)
)

// Releases returns the parsed releases from VersionsYAML.
func (s *Spec) Releases() ([]toolversions.Release, error) {
	if len(s.VersionsYAML) == 0 {
		return nil, nil
	}
	key := s.Name + ":" + string(s.VersionsYAML)
	releasesCacheMu.Lock()
	defer releasesCacheMu.Unlock()
	if releases, ok := releasesCache[key]; ok {
		return releases, nil
	}
	releases, err := toolversions.Parse(s.VersionsYAML, s.Name)
	if err != nil {
		return nil, err
	}
	releasesCache[key] = releases
	return releases, nil
}

// LatestRelease returns the newest known release from VersionsYAML, or nil if empty.
func (s *Spec) LatestRelease() *toolversions.Release {
	releases, err := s.Releases()
	if err != nil || len(releases) == 0 {
		return nil
	}
	return toolversions.Latest(releases)
}

// LookupRelease finds a release by version string.
func (s *Spec) LookupRelease(version string) (*toolversions.Release, bool) {
	releases, err := s.Releases()
	if err != nil {
		return nil, false
	}
	return toolversions.Lookup(releases, version)
}

// NormalizeTag ensures that the version string has a leading "v".
func NormalizeTag(version string) string {
	v := strings.TrimSpace(version)
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}

// LoadConfig loads and validates the tool configuration from .ap/<spec.Name>.yaml in repoRoot.
// If the configuration file does not exist, it returns "", nil.
func LoadConfig(spec *Spec, repoRoot string) (string, error) {
	if repoRoot == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		repoRoot = wd
	}
	configFile := filepath.Join(repoRoot, ".ap", spec.Name+".yaml")

	data, err := os.ReadFile(configFile)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("error reading %s: %w", configFile, err)
	}

	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return "", fmt.Errorf("invalid %s: %w", configFile, err)
	}

	for k := range raw {
		if k != spec.Name {
			return "", fmt.Errorf("invalid %s: unknown key %q", configFile, k)
		}
	}

	var version string
	if value, ok := raw[spec.Name]; ok && value != nil {
		valueString, isString := value.(string)
		if !isString {
			return "", fmt.Errorf("invalid %s: %s must be a string", configFile, spec.Name)
		}
		version = strings.TrimSpace(valueString)
	}

	if version == "" || version == "latest" {
		if release := spec.LatestRelease(); release != nil {
			version = release.Version
		}
	}

	return version, nil
}

// LoadConfig loads and validates the tool configuration from .ap/<spec.Name>.yaml in repoRoot.
func (s *Spec) LoadConfig(repoRoot string) (string, error) {
	return LoadConfig(s, repoRoot)
}

var ensureLocks sync.Map

func getEnsureLock(name string) *sync.Mutex {
	v, _ := ensureLocks.LoadOrStore(name, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// Ensure ensures that the tool binary is downloaded, verified against checksum,
// and cached in AP_CACHE_DIR/tools/<Name>/<tag>/bin/<Binary>. Returns the binary path.
func Ensure(ctx context.Context, spec *Spec, version string) (string, error) {
	mu := getEnsureLock(spec.Name)
	mu.Lock()
	defer mu.Unlock()

	cacheRoot, err := fileutils.CacheDir()
	if err != nil {
		return "", fmt.Errorf("failed to determine cache dir: %w", err)
	}

	binName := spec.BinaryName()

	// Fast path for pinned versions already cached and executable
	if version != "" && version != "latest" {
		tag := NormalizeTag(version)
		binPath := filepath.Join(cacheRoot, "tools", spec.Name, tag, "bin", binName)
		if fileutils.IsExecutable(binPath) {
			klog.V(2).Infof("Using %s %s", spec.Name, tag)
			return binPath, nil
		}
	}

	release, err := ResolveRelease(ctx, spec, version)
	if err != nil {
		return "", err
	}

	tag := NormalizeTag(release.Version)
	binPath := filepath.Join(cacheRoot, "tools", spec.Name, tag, "bin", binName)
	if fileutils.IsExecutable(binPath) {
		klog.V(2).Infof("Using %s %s", spec.Name, tag)
		return binPath, nil
	}

	tmpFile, err := fetch.Download(ctx, release.URL, release.Digest, cacheRoot)
	if err != nil {
		return "", fmt.Errorf("failed to download %s from %s: %w", spec.Name, release.URL, err)
	}
	defer func() {
		if rmErr := os.Remove(tmpFile); rmErr != nil && !os.IsNotExist(rmErr) {
			klog.V(2).Infof("failed to remove temporary file %s: %v", tmpFile, rmErr)
		}
	}()

	if err := spec.Archive.Extract(tmpFile, binName, binPath); err != nil {
		return "", fmt.Errorf("failed to extract %s: %w", spec.Name, err)
	}

	if err := os.Chmod(binPath, 0755); err != nil {
		return "", fmt.Errorf("failed to chmod %s binary %s: %w", spec.Name, binPath, err)
	}

	klog.Infof("Installed %s %s to %s", spec.Name, tag, binPath)
	return binPath, nil
}

// Ensure ensures that the tool binary is downloaded, verified against checksum, and cached.
func (s *Spec) Ensure(ctx context.Context, version string) (string, error) {
	return Ensure(ctx, s, version)
}

// EnsureForRepo loads .ap/<Name>.yaml in repoRoot (if present) and ensures the pinned or default binary.
func EnsureForRepo(ctx context.Context, spec *Spec, repoRoot string) (string, error) {
	version, err := LoadConfig(spec, repoRoot)
	if err != nil {
		return "", err
	}
	return Ensure(ctx, spec, version)
}

// EnsureForRepo loads .ap/<Name>.yaml in repoRoot (if present) and ensures the pinned or default binary.
func (s *Spec) EnsureForRepo(ctx context.Context, repoRoot string) (string, error) {
	return EnsureForRepo(ctx, s, repoRoot)
}

// ResolvedRelease contains the resolved version tag, download URL, and expected digest.
type ResolvedRelease struct {
	Version string
	URL     string
	Digest  string
}

// ResolveRelease resolves the release version, download URL, and expected digest for a spec.
func ResolveRelease(ctx context.Context, spec *Spec, version string) (*ResolvedRelease, error) {
	platform := runtime.GOOS + "-" + runtime.GOARCH

	if len(spec.Platforms) > 0 {
		supported := false
		for _, p := range spec.Platforms {
			if p == platform {
				supported = true
				break
			}
		}
		if !supported {
			return nil, fmt.Errorf("unsupported platform %s for %s", platform, spec.Name)
		}
	}

	requestedVersion := strings.TrimSpace(version)
	if requestedVersion == "" || requestedVersion == "latest" {
		release := spec.LatestRelease()
		if release == nil {
			return nil, fmt.Errorf("no %s releases found in versions.yaml", spec.Name)
		}
		digest, ok := release.Assets[platform]
		if !ok || digest == "" {
			return nil, fmt.Errorf("%s %s has no asset for %s in versions.yaml", spec.Name, release.Version, platform)
		}
		return &ResolvedRelease{
			Version: release.Version,
			URL:     spec.DownloadURL(release.Version, runtime.GOOS, runtime.GOARCH),
			Digest:  digest,
		}, nil
	}

	if release, ok := spec.LookupRelease(requestedVersion); ok {
		digest, ok := release.Assets[platform]
		if !ok || digest == "" {
			return nil, fmt.Errorf("%s %s has no asset for %s in versions.yaml", spec.Name, release.Version, platform)
		}
		return &ResolvedRelease{
			Version: release.Version,
			URL:     spec.DownloadURL(release.Version, runtime.GOOS, runtime.GOARCH),
			Digest:  digest,
		}, nil
	}

	// On a miss: warn and resolve from source
	klog.Warningf("%s %s is not in versions.yaml; resolving it from the GitHub API, which is rate limited; run 'ap tool update-versions' to add it", spec.Name, requestedVersion)
	tag := NormalizeTag(requestedVersion)
	url := spec.DownloadURL(tag, runtime.GOOS, runtime.GOARCH)

	digest, err := spec.Source.Digest(ctx, tag, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return nil, err
	}

	return &ResolvedRelease{
		Version: tag,
		URL:     url,
		Digest:  digest,
	}, nil
}

// Update fetches newest releases from spec.Source and updates versions.yaml newest first.
func Update(ctx context.Context, spec *Spec, repoRoot string) error {
	if repoRoot == "" {
		return fmt.Errorf("repoRoot is required for Update")
	}

	versions, err := spec.Source.Versions(ctx)
	if err != nil {
		return fmt.Errorf("failed to list %s versions: %w", spec.Name, err)
	}

	const maxReleases = 5
	var releases []toolversions.Release
	seenVersions := make(map[string]bool)

	for _, version := range versions {
		if seenVersions[version] {
			continue
		}

		assetsMap := make(map[string]string)
		for _, plat := range spec.Platforms {
			parts := strings.Split(plat, "-")
			if len(parts) != 2 {
				continue
			}
			goos, goarch := parts[0], parts[1]

			digest, err := spec.Source.Digest(ctx, version, goos, goarch)
			if err != nil || digest == "" {
				klog.V(2).Infof("failed to get digest for %s %s on %s: %v", spec.Name, version, plat, err)
				break
			}
			assetsMap[plat] = digest
		}

		if len(assetsMap) == len(spec.Platforms) {
			releases = append(releases, toolversions.Release{
				Version: version,
				Assets:  assetsMap,
			})
			seenVersions[version] = true
			if len(releases) == maxReleases {
				break
			}
		}
	}

	if len(releases) == 0 {
		return fmt.Errorf("no valid releases found for %s", spec.Name)
	}

	toolversions.SortNewestFirst(releases)
	if len(releases) > maxReleases {
		releases = releases[:maxReleases]
	}

	data, err := yaml.Marshal(map[string][]toolversions.Release{
		spec.Name: releases,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal %s versions data: %w", spec.Name, err)
	}

	targetPath := spec.VersionsPath
	if targetPath == "" {
		targetPath = filepath.Join("ap", "pkg", "tool", spec.Name, "versions.yaml")
	}
	if !filepath.IsAbs(targetPath) {
		targetPath = filepath.Join(repoRoot, targetPath)
	}

	if err := fileutils.WriteFileAtomic(targetPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", targetPath, err)
	}

	return nil
}

// Update updates versions.yaml for this spec.
func (s *Spec) Update(ctx context.Context, repoRoot string) error {
	return Update(ctx, s, repoRoot)
}
