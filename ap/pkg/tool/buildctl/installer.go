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

package buildctl

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

var buildctlMu sync.Mutex

// EnsureBuildctl ensures that the buildctl binary is downloaded, verified against checksum,
// extracted from the buildkit tarball, and cached in AP_CACHE_DIR/tools/buildctl/<tag>/bin/buildctl.
// Returns the path to the executable binary.
func EnsureBuildctl(ctx context.Context, version string) (string, error) {
	buildctlMu.Lock()
	defer buildctlMu.Unlock()

	cacheRoot, err := fileutils.CacheDir()
	if err != nil {
		return "", fmt.Errorf("failed to determine cache dir: %w", err)
	}

	binName := "buildctl"
	if runtime.GOOS == "windows" {
		binName = "buildctl.exe"
	}

	// Fast path for pinned versions already cached and executable
	if version != "" && version != "latest" {
		tag := NormalizeTag(version)
		installDir := filepath.Join(cacheRoot, "tools", "buildctl", tag)
		binPath := filepath.Join(installDir, "bin", binName)
		if fileutils.IsExecutable(binPath) {
			klog.V(2).Infof("Using buildctl %s", tag)
			return binPath, nil
		}
	}

	resolvedVer, downloadURL, expectedDigest, err := resolveBuildctlRelease(ctx, version)
	if err != nil {
		return "", err
	}

	tag := NormalizeTag(resolvedVer)
	installDir := filepath.Join(cacheRoot, "tools", "buildctl", tag)
	binPath := filepath.Join(installDir, "bin", binName)
	if fileutils.IsExecutable(binPath) {
		klog.V(2).Infof("Using buildctl %s", tag)
		return binPath, nil
	}

	if envDigest := os.Getenv("BUILDCTL_DOWNLOAD_DIGEST"); envDigest != "" {
		expectedDigest = envDigest
	}
	if baseURL := os.Getenv("BUILDCTL_DOWNLOAD_BASE_URL"); baseURL != "" {
		var urlErr error
		downloadURL, urlErr = ResolveBuildctlDownloadURL(baseURL, tag, runtime.GOOS, runtime.GOARCH)
		if urlErr != nil {
			return "", urlErr
		}
	}

	tmpArchive, err := fetch.Download(ctx, downloadURL, expectedDigest, cacheRoot)
	if err != nil {
		return "", fmt.Errorf("failed to download buildctl archive from %s: %w", downloadURL, err)
	}
	defer func() {
		if rmErr := os.Remove(tmpArchive); rmErr != nil && !os.IsNotExist(rmErr) {
			klog.V(2).Infof("failed to remove temporary archive %s: %v", tmpArchive, rmErr)
		}
	}()

	if err := extractBuildctlTarGz(tmpArchive, binPath, binName); err != nil {
		return "", fmt.Errorf("failed to extract %s from %s: %w", binName, tmpArchive, err)
	}

	if err := os.Chmod(binPath, 0755); err != nil {
		return "", fmt.Errorf("failed to chmod buildctl binary %s: %w", binPath, err)
	}

	klog.Infof("Installed buildctl %s to %s", tag, binPath)
	return binPath, nil
}

// EnsureBuildctlForRepo loads .ap/buildctl.yaml in repoRoot (if present) and ensures the pinned or default buildctl binary.
func EnsureBuildctlForRepo(ctx context.Context, repoRoot string) (string, error) {
	var version string
	if repoRoot != "" {
		cfg, err := LoadConfig(repoRoot)
		if err != nil {
			return "", err
		}
		if cfg != nil {
			version = cfg.Buildctl
		}
	}
	return EnsureBuildctl(ctx, version)
}

func extractBuildctlTarGz(tarGzPath, dstBin, targetName string) error {
	file, err := os.Open(tarGzPath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			klog.V(2).Infof("failed to close %s: %v", tarGzPath, closeErr)
		}
	}()

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
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
			return err
		}
		cleanName := filepath.Clean(header.Name)
		if header.Typeflag == tar.TypeReg && (cleanName == targetName || cleanName == filepath.Join("bin", targetName) || strings.HasSuffix(cleanName, string(filepath.Separator)+targetName) || strings.HasSuffix(cleanName, "/"+targetName)) {
			return extractTarEntry(tarReader, dstBin)
		}
	}
	return fmt.Errorf("target binary %q not found in archive %s", targetName, tarGzPath)
}

func extractTarEntry(tarReader *tar.Reader, dstBin string) error {
	dstDir := filepath.Dir(dstBin)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dstDir, err)
	}

	tmpFile, err := os.CreateTemp(dstDir, ".buildctl-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %w", err)
	}
	tmpName := tmpFile.Name()
	success := false
	defer func() {
		if !success {
			if closeErr := tmpFile.Close(); closeErr != nil {
				klog.V(2).Infof("failed to close temporary file: %v", closeErr)
			}
			if rmErr := os.Remove(tmpName); rmErr != nil && !os.IsNotExist(rmErr) {
				klog.V(2).Infof("failed to remove temporary file %s: %v", tmpName, rmErr)
			}
		}
	}()

	if _, err := io.Copy(tmpFile, tarReader); err != nil {
		return fmt.Errorf("failed to extract from tar: %w", err)
	}
	if err := tmpFile.Chmod(0755); err != nil {
		return fmt.Errorf("failed to chmod temporary binary: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temporary binary: %w", err)
	}
	if err := os.Rename(tmpName, dstBin); err != nil {
		return fmt.Errorf("failed to rename %s to %s: %w", tmpName, dstBin, err)
	}
	success = true
	return nil
}

func resolveBuildctlRelease(ctx context.Context, version string) (resolvedVer, downloadURL, expectedDigest string, err error) {
	requestedVer := strings.TrimSpace(version)
	baseURL := os.Getenv("BUILDCTL_DOWNLOAD_BASE_URL")

	if requestedVer == "" || requestedVer == "latest" {
		rel := LatestBuildctl()
		if rel == nil {
			return "", "", "", fmt.Errorf("no buildctl releases found in versions.yaml")
		}
		resolvedVer = rel.Version
		digest, err := ResolveBuildctlDigest(rel, runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return "", "", "", err
		}
		downloadURL, err := ResolveBuildctlDownloadURL(baseURL, resolvedVer, runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return "", "", "", err
		}
		return resolvedVer, downloadURL, digest, nil
	}

	if rel, ok := LookupBuildctl(requestedVer); ok {
		resolvedVer = rel.Version
		digest, err := ResolveBuildctlDigest(rel, runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return "", "", "", err
		}
		downloadURL, err := ResolveBuildctlDownloadURL(baseURL, resolvedVer, runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return "", "", "", err
		}
		return resolvedVer, downloadURL, digest, nil
	}

	// On a miss: warn and resolve from GitHub API
	klog.Warningf("buildctl %s is not in versions.yaml; resolving it from the GitHub API, which is rate limited; run 'ap tool update-versions' to add it", requestedVer)
	return resolvePinnedBuildctlRelease(ctx, requestedVer)
}

func resolvePinnedBuildctlRelease(ctx context.Context, version string) (resolvedVer, downloadURL, expectedDigest string, err error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, github.APITimeout)
		defer cancel()
	}

	tag := NormalizeTag(version)
	assetName, err := ResolveBuildctlAsset(tag, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", "", "", err
	}

	baseURL := os.Getenv("BUILDCTL_DOWNLOAD_BASE_URL")
	if baseURL != "" {
		return tag, fmt.Sprintf("%s/%s", strings.TrimRight(baseURL, "/"), assetName), os.Getenv("BUILDCTL_DOWNLOAD_DIGEST"), nil
	}

	apiBase := os.Getenv("BUILDCTL_GITHUB_API_BASE_URL")
	if apiBase == "" {
		apiBase = os.Getenv("GITHUB_API_BASE_URL")
	}
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}

	tagURL := fmt.Sprintf("%s/repos/moby/buildkit/releases/tags/%s", strings.TrimRight(apiBase, "/"), tag)
	rel, err := github.FetchRelease(ctx, tagURL)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to fetch release for buildctl %s: %w", tag, err)
	}

	asset, found := github.FindAsset(rel.Assets, assetName)
	if !found {
		return "", "", "", fmt.Errorf("release %s has no asset %s", tag, assetName)
	}

	digest := asset.Digest
	if digest == "" {
		provenanceName := strings.TrimSuffix(assetName, ".tar.gz") + ".provenance.json"
		if provenanceAsset, ok := github.FindAsset(rel.Assets, provenanceName); ok && provenanceAsset.BrowserDownloadURL != "" {
			jsonText, err := github.GetText(ctx, provenanceAsset.BrowserDownloadURL)
			if err != nil {
				klog.V(2).Infof("failed to fetch provenance asset %s: %v", provenanceAsset.BrowserDownloadURL, err)
			} else {
				parsedDigest := extractDigestFromProvenance(jsonText, assetName)
				if parsedDigest != "" {
					digest = parsedDigest
				}
			}
		}
	}

	if digest == "" && asset.BrowserDownloadURL != "" {
		computedDigest, err := fetch.Digest(ctx, asset.BrowserDownloadURL)
		if err != nil {
			klog.V(2).Infof("failed computing digest for %s: %v", asset.BrowserDownloadURL, err)
		} else if computedDigest != "" {
			digest = computedDigest
		}
	}

	return tag, asset.BrowserDownloadURL, digest, nil
}

type provenanceStatement struct {
	Subject []struct {
		Name   string            `json:"name"`
		Digest map[string]string `json:"digest"`
	} `json:"subject"`
}

func extractDigestFromProvenance(provenanceJSON, assetName string) string {
	var statement provenanceStatement
	if err := json.Unmarshal([]byte(provenanceJSON), &statement); err != nil {
		return ""
	}
	for _, subject := range statement.Subject {
		if subject.Name == assetName {
			if sha256Hex, ok := subject.Digest["sha256"]; ok && sha256Hex != "" {
				if !strings.HasPrefix(sha256Hex, "sha256:") {
					return "sha256:" + sha256Hex
				}
				return sha256Hex
			}
		}
	}
	return ""
}
