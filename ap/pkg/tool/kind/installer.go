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

package kind

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/fetch"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/github"
	"k8s.io/klog/v2"
)

var kindMu sync.Mutex

// NormalizeTag ensures that the version string has a leading "v".
func NormalizeTag(version string) string {
	v := strings.TrimSpace(version)
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}

// ResolveKindDownloadURL computes the download URL for kind release binaries.
func ResolveKindDownloadURL(baseURL, tag, goos, goarch string) (string, error) {
	normTag := NormalizeTag(tag)
	assetName, err := ResolveKindAsset(goos, goarch)
	if err != nil {
		return "", err
	}
	if baseURL == "" {
		baseURL = fmt.Sprintf("https://github.com/kubernetes-sigs/kind/releases/download/%s", normTag)
	}
	return fmt.Sprintf("%s/%s", strings.TrimRight(baseURL, "/"), assetName), nil
}

// EnsureKind ensures that the kind binary is downloaded, verified against checksum,
// and cached in AP_CACHE_DIR/tools/kind/<tag>/bin/kind. Returns the path to the executable binary.
func EnsureKind(ctx context.Context, version string) (string, error) {
	kindMu.Lock()
	defer kindMu.Unlock()

	cacheRoot, err := fileutils.CacheDir()
	if err != nil {
		return "", fmt.Errorf("failed to determine cache dir: %w", err)
	}

	binName := "kind"
	if runtime.GOOS == "windows" {
		binName = "kind.exe"
	}

	// Fast path for pinned versions already cached and executable
	if version != "" && version != "latest" {
		tag := NormalizeTag(version)
		installDir := filepath.Join(cacheRoot, "tools", "kind", tag)
		binPath := filepath.Join(installDir, "bin", binName)
		if fileutils.IsExecutable(binPath) {
			klog.V(2).Infof("Using kind %s", tag)
			return binPath, nil
		}
	}

	resolvedVer, downloadURL, expectedDigest, err := resolveKindRelease(ctx, version)
	if err != nil {
		return "", err
	}

	tag := NormalizeTag(resolvedVer)
	installDir := filepath.Join(cacheRoot, "tools", "kind", tag)
	binPath := filepath.Join(installDir, "bin", binName)
	if fileutils.IsExecutable(binPath) {
		klog.V(2).Infof("Using kind %s", tag)
		return binPath, nil
	}

	if envDigest := os.Getenv("KIND_DOWNLOAD_DIGEST"); envDigest != "" {
		expectedDigest = envDigest
	}
	if baseURL := os.Getenv("KIND_DOWNLOAD_BASE_URL"); baseURL != "" {
		assetName, assetErr := ResolveKindAsset(runtime.GOOS, runtime.GOARCH)
		if assetErr != nil {
			return "", assetErr
		}
		downloadURL = fmt.Sprintf("%s/%s", strings.TrimRight(baseURL, "/"), assetName)
	}

	tmpFile, err := fetch.Download(ctx, downloadURL, expectedDigest, cacheRoot)
	if err != nil {
		return "", fmt.Errorf("failed to download kind from %s: %w", downloadURL, err)
	}
	defer func() {
		if rmErr := os.Remove(tmpFile); rmErr != nil && !os.IsNotExist(rmErr) {
			klog.V(2).Infof("failed to remove temporary file %s: %v", tmpFile, rmErr)
		}
	}()

	if err := os.MkdirAll(filepath.Dir(binPath), 0755); err != nil {
		return "", fmt.Errorf("failed to create directory %s: %w", filepath.Dir(binPath), err)
	}

	if err := fileutils.CopyFile(tmpFile, binPath, 0755); err != nil {
		return "", fmt.Errorf("failed to install kind binary %s: %w", binPath, err)
	}

	if err := os.Chmod(binPath, 0755); err != nil {
		return "", fmt.Errorf("failed to chmod kind binary %s: %w", binPath, err)
	}

	klog.Infof("Installed kind %s to %s", tag, binPath)
	return binPath, nil
}

// EnsureKindForRepo loads .ap/kind.yaml in repoRoot (if present) and ensures the pinned or default kind binary.
func EnsureKindForRepo(ctx context.Context, repoRoot string) (string, error) {
	var version string
	if repoRoot != "" {
		cfg, err := LoadConfig(repoRoot)
		if err != nil {
			return "", err
		}
		if cfg != nil {
			version = cfg.Kind
		}
	}
	return EnsureKind(ctx, version)
}

func resolveKindRelease(ctx context.Context, version string) (resolvedVer, downloadURL, expectedDigest string, err error) {
	requestedVer := strings.TrimSpace(version)
	if requestedVer == "" || requestedVer == "latest" {
		rel := LatestKind()
		if rel == nil {
			return "", "", "", fmt.Errorf("no kind releases found in versions.yaml")
		}
		resolvedVer = rel.Version
		assetName, err := ResolveKindAsset(runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return "", "", "", err
		}
		digest, ok := rel.Assets[assetName]
		if !ok || digest == "" {
			return "", "", "", fmt.Errorf("kind %s has no asset %s in versions.yaml", resolvedVer, assetName)
		}
		baseURL := os.Getenv("KIND_DOWNLOAD_BASE_URL")
		if baseURL == "" {
			baseURL = fmt.Sprintf("https://github.com/kubernetes-sigs/kind/releases/download/%s", NormalizeTag(resolvedVer))
		}
		return resolvedVer, fmt.Sprintf("%s/%s", strings.TrimRight(baseURL, "/"), assetName), digest, nil
	}

	if rel, ok := LookupKind(requestedVer); ok {
		resolvedVer = rel.Version
		assetName, err := ResolveKindAsset(runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return "", "", "", err
		}
		digest, ok := rel.Assets[assetName]
		if !ok || digest == "" {
			return "", "", "", fmt.Errorf("kind %s has no asset %s in versions.yaml", resolvedVer, assetName)
		}
		baseURL := os.Getenv("KIND_DOWNLOAD_BASE_URL")
		if baseURL == "" {
			baseURL = fmt.Sprintf("https://github.com/kubernetes-sigs/kind/releases/download/%s", NormalizeTag(resolvedVer))
		}
		return resolvedVer, fmt.Sprintf("%s/%s", strings.TrimRight(baseURL, "/"), assetName), digest, nil
	}

	// On a miss: warn and resolve from GitHub API
	klog.Warningf("kind %s is not in versions.yaml; resolving it from the GitHub API, which is rate limited; run 'ap tool update-versions' to add it", requestedVer)
	return resolvePinnedKindRelease(ctx, requestedVer)
}

func resolvePinnedKindRelease(ctx context.Context, version string) (resolvedVer, downloadURL, expectedDigest string, err error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, github.APITimeout)
		defer cancel()
	}

	tag := NormalizeTag(version)
	assetName, err := ResolveKindAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", "", "", err
	}

	baseURL := os.Getenv("KIND_DOWNLOAD_BASE_URL")
	if baseURL != "" {
		return tag, fmt.Sprintf("%s/%s", strings.TrimRight(baseURL, "/"), assetName), os.Getenv("KIND_DOWNLOAD_DIGEST"), nil
	}

	apiBase := os.Getenv("KIND_GITHUB_API_BASE_URL")
	if apiBase == "" {
		apiBase = os.Getenv("GITHUB_API_BASE_URL")
	}
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}

	tagURL := fmt.Sprintf("%s/repos/kubernetes-sigs/kind/releases/tags/%s", strings.TrimRight(apiBase, "/"), tag)
	rel, err := github.FetchRelease(ctx, tagURL)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to fetch release for kind %s: %w", tag, err)
	}

	asset, found := github.FindAsset(rel.Assets, assetName)
	if !found {
		return "", "", "", fmt.Errorf("release %s has no asset %s", tag, assetName)
	}

	digest := asset.Digest
	if digest == "" {
		if sumAsset, ok := github.FindAsset(rel.Assets, assetName+".sha256sum"); ok && sumAsset.BrowserDownloadURL != "" {
			txt, err := github.GetText(ctx, sumAsset.BrowserDownloadURL)
			if err != nil {
				klog.V(2).Infof("failed to fetch kind checksum asset %s: %v", sumAsset.BrowserDownloadURL, err)
			} else {
				fields := strings.Fields(txt)
				if len(fields) >= 1 {
					digest = "sha256:" + fields[0]
				}
			}
		}
	}
	if digest == "" && asset.BrowserDownloadURL != "" {
		d, err := fetch.Digest(ctx, asset.BrowserDownloadURL)
		if err != nil {
			klog.V(2).Infof("failed computing digest for %s: %v", asset.BrowserDownloadURL, err)
		} else if d != "" {
			digest = d
		}
	}

	return tag, asset.BrowserDownloadURL, digest, nil
}
