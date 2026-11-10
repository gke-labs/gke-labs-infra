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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
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

// Source provides versions, asset URLs, and digests for a tool.
type Source interface {
	Versions(ctx context.Context) ([]string, error)
	AssetURL(tag, goos, goarch string) string
	Digest(ctx context.Context, tag, goos, goarch string) (string, error)
}

// GitHubReleases fetches versions from GitHub releases and computes digests via fetch.Digest.
type GitHubReleases struct {
	Repo       string
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

	rawReleases, err := github.ListReleases(ctx, apiBase, g.Repo, 20)
	if err != nil {
		return nil, fmt.Errorf("failed to list %s releases: %w", g.Repo, err)
	}

	var vers []string
	seen := make(map[string]bool)
	for _, rel := range rawReleases {
		if rel.Draft || rel.Prerelease {
			continue
		}
		ver := strings.TrimPrefix(rel.TagName, "v")
		if ver == "" || ver[0] < '0' || ver[0] > '9' || strings.Contains(ver, "-") {
			continue
		}
		if seen[rel.TagName] {
			continue
		}
		seen[rel.TagName] = true
		vers = append(vers, rel.TagName)
	}

	sort.Slice(vers, func(i, j int) bool {
		return toolversions.Compare(vers[i], vers[j]) > 0
	})

	return vers, nil
}

// AssetURL returns the download URL for the asset on GitHub releases.
func (g *GitHubReleases) AssetURL(tag, goos, goarch string) string {
	asset := g.Asset(tag, goos, goarch)
	if g.BaseURL != "" {
		return fmt.Sprintf("%s/%s", strings.TrimRight(g.BaseURL, "/"), asset)
	}
	normTag := NormalizeTag(tag)
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", g.Repo, normTag, asset)
}

// Digest downloads the asset and computes its SHA-256 digest using fetch.Digest.
func (g *GitHubReleases) Digest(ctx context.Context, tag, goos, goarch string) (string, error) {
	url := g.AssetURL(tag, goos, goarch)
	return fetch.Digest(ctx, url)
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
	if rels, ok := releasesCache[key]; ok {
		return rels, nil
	}
	rels, err := toolversions.Parse(s.VersionsYAML, s.Name)
	if err != nil {
		return nil, err
	}
	releasesCache[key] = rels
	return rels, nil
}

// LatestRelease returns the newest known release from VersionsYAML, or nil if empty.
func (s *Spec) LatestRelease() *toolversions.Release {
	rels, err := s.Releases()
	if err != nil || len(rels) == 0 {
		return nil
	}
	return toolversions.Latest(rels)
}

// LookupRelease finds a release by version string.
func (s *Spec) LookupRelease(version string) (*toolversions.Release, bool) {
	rels, err := s.Releases()
	if err != nil {
		return nil, false
	}
	return toolversions.Lookup(rels, version)
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
	if val, ok := raw[spec.Name]; ok && val != nil {
		valStr, isStr := val.(string)
		if !isStr {
			return "", fmt.Errorf("invalid %s: %s must be a string", configFile, spec.Name)
		}
		version = strings.TrimSpace(valStr)
	}

	if version == "" || version == "latest" {
		if rel := spec.LatestRelease(); rel != nil {
			version = rel.Version
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

	rel, err := ResolveRelease(ctx, spec, version)
	if err != nil {
		return "", err
	}

	tag := NormalizeTag(rel.Version)
	binPath := filepath.Join(cacheRoot, "tools", spec.Name, tag, "bin", binName)
	if fileutils.IsExecutable(binPath) {
		klog.V(2).Infof("Using %s %s", spec.Name, tag)
		return binPath, nil
	}

	tmpFile, err := fetch.Download(ctx, rel.URL, rel.Digest, cacheRoot)
	if err != nil {
		return "", fmt.Errorf("failed to download %s from %s: %w", spec.Name, rel.URL, err)
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
	ver, err := LoadConfig(spec, repoRoot)
	if err != nil {
		return "", err
	}
	return Ensure(ctx, spec, ver)
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

	requestedVer := strings.TrimSpace(version)
	if requestedVer == "" || requestedVer == "latest" {
		rel := spec.LatestRelease()
		if rel == nil {
			return nil, fmt.Errorf("no %s releases found in versions.yaml", spec.Name)
		}
		digest, ok := rel.Assets[platform]
		if !ok || digest == "" {
			return nil, fmt.Errorf("%s %s has no asset for %s in versions.yaml", spec.Name, rel.Version, platform)
		}
		return &ResolvedRelease{
			Version: rel.Version,
			URL:     spec.DownloadURL(rel.Version, runtime.GOOS, runtime.GOARCH),
			Digest:  digest,
		}, nil
	}

	if rel, ok := spec.LookupRelease(requestedVer); ok {
		digest, ok := rel.Assets[platform]
		if !ok || digest == "" {
			return nil, fmt.Errorf("%s %s has no asset for %s in versions.yaml", spec.Name, rel.Version, platform)
		}
		return &ResolvedRelease{
			Version: rel.Version,
			URL:     spec.DownloadURL(rel.Version, runtime.GOOS, runtime.GOARCH),
			Digest:  digest,
		}, nil
	}

	// On a miss: warn and resolve from source
	klog.Warningf("%s %s is not in versions.yaml; resolving it from the GitHub API, which is rate limited; run 'ap tool update-versions' to add it", spec.Name, requestedVer)
	tag := NormalizeTag(requestedVer)
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

	vers, err := spec.Source.Versions(ctx)
	if err != nil {
		return fmt.Errorf("failed to list %s versions: %w", spec.Name, err)
	}

	const maxReleases = 5
	var releases []toolversions.Release
	seenVersions := make(map[string]bool)

	for _, ver := range vers {
		if seenVersions[ver] {
			continue
		}

		assetsMap := make(map[string]string)
		for _, plat := range spec.Platforms {
			parts := strings.Split(plat, "-")
			if len(parts) != 2 {
				continue
			}
			goos, goarch := parts[0], parts[1]

			digest, err := spec.Source.Digest(ctx, ver, goos, goarch)
			if err != nil || digest == "" {
				klog.V(2).Infof("failed to get digest for %s %s on %s: %v", spec.Name, ver, plat, err)
				break
			}
			assetsMap[plat] = digest
		}

		if len(assetsMap) == len(spec.Platforms) {
			releases = append(releases, toolversions.Release{
				Version: ver,
				Assets:  assetsMap,
			})
			seenVersions[ver] = true
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
