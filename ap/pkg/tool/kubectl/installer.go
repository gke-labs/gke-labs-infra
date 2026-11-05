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

package kubectl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/fetch"
	"k8s.io/klog/v2"
)

var kubectlMu sync.Mutex

// EnsureKubectl ensures that the kubectl binary is downloaded, verified against checksum,
// and cached in AP_CACHE_DIR/tools/kubectl/<tag>/bin/kubectl. Returns the path to the executable binary.
func EnsureKubectl(ctx context.Context, version string) (string, error) {
	kubectlMu.Lock()
	defer kubectlMu.Unlock()

	cacheRoot, err := fileutils.CacheDir()
	if err != nil {
		return "", fmt.Errorf("failed to determine cache dir: %w", err)
	}

	binName := "kubectl"
	if runtime.GOOS == "windows" {
		binName = "kubectl.exe"
	}

	// Fast path for pinned versions already cached and executable
	if version != "" && version != "latest" {
		tag := NormalizeTag(version)
		installDir := filepath.Join(cacheRoot, "tools", "kubectl", tag)
		binPath := filepath.Join(installDir, "bin", binName)
		if fileutils.IsExecutable(binPath) {
			klog.V(2).Infof("Using kubectl %s", tag)
			return binPath, nil
		}
	}

	resolvedVer, downloadURL, expectedDigest, err := resolveKubectlRelease(ctx, version)
	if err != nil {
		return "", err
	}

	tag := NormalizeTag(resolvedVer)
	installDir := filepath.Join(cacheRoot, "tools", "kubectl", tag)
	binPath := filepath.Join(installDir, "bin", binName)
	if fileutils.IsExecutable(binPath) {
		klog.V(2).Infof("Using kubectl %s", tag)
		return binPath, nil
	}

	if envDigest := os.Getenv("KUBECTL_DOWNLOAD_DIGEST"); envDigest != "" {
		expectedDigest = envDigest
	}
	if baseURL := os.Getenv("KUBECTL_DOWNLOAD_BASE_URL"); baseURL != "" {
		var urlErr error
		downloadURL, urlErr = ResolveKubectlDownloadURL(baseURL, tag, runtime.GOOS, runtime.GOARCH)
		if urlErr != nil {
			return "", urlErr
		}
	}

	tmpFile, err := fetch.Download(ctx, downloadURL, expectedDigest, cacheRoot)
	if err != nil {
		return "", fmt.Errorf("failed to download kubectl from %s: %w", downloadURL, err)
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
		return "", fmt.Errorf("failed to install kubectl binary %s: %w", binPath, err)
	}

	if err := os.Chmod(binPath, 0755); err != nil {
		return "", fmt.Errorf("failed to chmod kubectl binary %s: %w", binPath, err)
	}

	klog.Infof("Installed kubectl %s to %s", tag, binPath)
	return binPath, nil
}

// EnsureKubectlForRepo loads .ap/kubectl.yaml in repoRoot (if present) and ensures the pinned or default kubectl binary.
func EnsureKubectlForRepo(ctx context.Context, repoRoot string) (string, error) {
	var version string
	if repoRoot != "" {
		cfg, err := LoadConfig(repoRoot)
		if err != nil {
			return "", err
		}
		if cfg != nil {
			version = cfg.Kubectl
		}
	}
	return EnsureKubectl(ctx, version)
}

func resolveKubectlRelease(ctx context.Context, version string) (resolvedVer, downloadURL, expectedDigest string, err error) {
	requestedVer := strings.TrimSpace(version)
	baseURL := os.Getenv("KUBECTL_DOWNLOAD_BASE_URL")
	if baseURL == "" {
		baseURL = "https://dl.k8s.io/release"
	}

	if requestedVer == "" || requestedVer == "latest" {
		rel := LatestKubectl()
		if rel == nil {
			return "", "", "", fmt.Errorf("no kubectl releases found in versions.yaml")
		}
		resolvedVer = rel.Version
		digest, err := ResolveKubectlDigest(rel, runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return "", "", "", err
		}
		downloadURL, err := ResolveKubectlDownloadURL(baseURL, resolvedVer, runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return "", "", "", err
		}
		return resolvedVer, downloadURL, digest, nil
	}

	if rel, ok := LookupKubectl(requestedVer); ok {
		resolvedVer = rel.Version
		digest, err := ResolveKubectlDigest(rel, runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return "", "", "", err
		}
		downloadURL, err := ResolveKubectlDownloadURL(baseURL, resolvedVer, runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return "", "", "", err
		}
		return resolvedVer, downloadURL, digest, nil
	}

	// On a miss: warn and resolve from dl.k8s.io
	klog.Warningf("kubectl %s is not in versions.yaml; resolving it from dl.k8s.io; run 'ap tool update-versions' to add it", requestedVer)
	return resolvePinnedKubectlRelease(ctx, baseURL, requestedVer)
}

func resolvePinnedKubectlRelease(ctx context.Context, baseURL, version string) (string, string, string, error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	tag := NormalizeTag(version)
	downloadURL, err := ResolveKubectlDownloadURL(baseURL, tag, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", "", "", err
	}

	shaURL, err := ResolveKubectlSha256URL(baseURL, tag, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", "", "", err
	}

	shaText, err := fetchText(ctx, shaURL)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to fetch checksum for kubectl %s from %s: %w", tag, shaURL, err)
	}

	fields := strings.Fields(shaText)
	if len(fields) == 0 {
		return "", "", "", fmt.Errorf("empty checksum file for kubectl %s at %s", tag, shaURL)
	}

	digest := fields[0]
	if !strings.HasPrefix(digest, "sha256:") {
		digest = "sha256:" + digest
	}

	return tag, downloadURL, digest, nil
}

func fetchText(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request for %s: %w", url, err)
	}
	req.Header.Set("User-Agent", "gke-labs-ap")

	resp, err := scoped.HTTPClient(ctx).Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch from %s: %w", url, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			klog.V(2).Infof("failed to close response body: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to fetch from %s: HTTP %s", url, resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response from %s: %w", url, err)
	}

	return strings.TrimSpace(string(body)), nil
}
