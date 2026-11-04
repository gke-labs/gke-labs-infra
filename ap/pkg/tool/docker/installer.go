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

package docker

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/fetch"
	"k8s.io/klog/v2"
)

var (
	dockerMu sync.Mutex
	buildxMu sync.Mutex
)

// ResolvePlatform maps runtime.GOOS and runtime.GOARCH to the supported platform identifier.
func ResolvePlatform(goos, goarch string) (string, error) {
	switch goos {
	case "linux":
		switch goarch {
		case "amd64", "arm64":
			return goos + "-" + goarch, nil
		default:
			return "", fmt.Errorf("unsupported linux architecture: %s", goarch)
		}
	case "darwin":
		switch goarch {
		case "amd64", "arm64":
			return goos + "-" + goarch, nil
		default:
			return "", fmt.Errorf("unsupported darwin architecture: %s", goarch)
		}
	case "windows":
		switch goarch {
		case "amd64":
			return goos + "-" + goarch, nil
		default:
			return "", fmt.Errorf("unsupported windows architecture: %s", goarch)
		}
	default:
		return "", fmt.Errorf("unsupported OS: %s", goos)
	}
}

// ResolveDockerDownloadURL computes the download URL for docker static binary archives.
func ResolveDockerDownloadURL(baseURL, version, goos, goarch string) (string, error) {
	var osPart, archPart, ext string
	switch goos {
	case "linux":
		osPart = "linux"
		ext = ".tgz"
		switch goarch {
		case "amd64":
			archPart = "x86_64"
		case "arm64":
			archPart = "aarch64"
		default:
			return "", fmt.Errorf("unsupported linux architecture: %s", goarch)
		}
	case "darwin":
		osPart = "mac"
		ext = ".tgz"
		switch goarch {
		case "amd64":
			archPart = "x86_64"
		case "arm64":
			archPart = "aarch64"
		default:
			return "", fmt.Errorf("unsupported darwin architecture: %s", goarch)
		}
	case "windows":
		osPart = "win"
		ext = ".zip"
		switch goarch {
		case "amd64":
			archPart = "x86_64"
		default:
			return "", fmt.Errorf("unsupported windows architecture: %s", goarch)
		}
	default:
		return "", fmt.Errorf("unsupported OS: %s", goos)
	}

	normVer := strings.TrimPrefix(version, "v")
	if baseURL == "" {
		baseURL = "https://download.docker.com"
	}
	return fmt.Sprintf("%s/%s/static/stable/%s/docker-%s%s", strings.TrimRight(baseURL, "/"), osPart, archPart, normVer, ext), nil
}

// ResolveBuildxDownloadURL computes the download URL for docker-buildx release binaries.
func ResolveBuildxDownloadURL(baseURL, version, goos, goarch string) (string, error) {
	tag := version
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	var ext string
	if goos == "windows" {
		ext = ".exe"
	}

	assetName := fmt.Sprintf("buildx-%s.%s-%s%s", tag, goos, goarch, ext)
	if baseURL == "" {
		baseURL = fmt.Sprintf("https://github.com/docker/buildx/releases/download/%s", tag)
	}
	return fmt.Sprintf("%s/%s", strings.TrimRight(baseURL, "/"), assetName), nil
}

// EnsureDocker ensures that the docker CLI binary is downloaded, verified against checksum,
// and cached in the tool cache directory. Returns the path to the executable binary.
func EnsureDocker(ctx context.Context, version string) (string, error) {
	dockerMu.Lock()
	defer dockerMu.Unlock()

	cacheRoot, err := fileutils.CacheDir()
	if err != nil {
		return "", fmt.Errorf("failed to determine cache dir: %w", err)
	}

	binName := "docker"
	if runtime.GOOS == "windows" {
		binName = "docker.exe"
	}

	resolvedVer := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if resolvedVer == "" || resolvedVer == "latest" {
		rel := LatestDocker()
		if rel == nil {
			return "", fmt.Errorf("no docker releases found in versions.yaml")
		}
		resolvedVer = rel.Version
	}

	installDir := filepath.Join(cacheRoot, "tools", "docker", resolvedVer)
	binPath := filepath.Join(installDir, "bin", binName)
	if fileutils.IsExecutable(binPath) {
		klog.V(2).Infof("Using docker %s", resolvedVer)
		return binPath, nil
	}

	plat, err := ResolvePlatform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}

	var expectedDigest string
	if rel, ok := LookupDocker(resolvedVer); ok {
		expectedDigest = rel.Assets[plat]
	} else {
		klog.Warningf("docker %s is not in versions.yaml; downloading without verified digest", resolvedVer)
	}

	if envDigest := os.Getenv("DOCKER_DOWNLOAD_DIGEST"); envDigest != "" {
		expectedDigest = envDigest
	}

	baseURL := os.Getenv("DOCKER_DOWNLOAD_BASE_URL")
	downloadURL, err := ResolveDockerDownloadURL(baseURL, resolvedVer, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}

	tmpArchive, err := fetch.Download(ctx, downloadURL, expectedDigest, cacheRoot)
	if err != nil {
		return "", fmt.Errorf("failed to download docker from %s: %w", downloadURL, err)
	}
	defer func() {
		if rmErr := os.Remove(tmpArchive); rmErr != nil && !os.IsNotExist(rmErr) {
			klog.V(2).Infof("failed to remove temporary archive %s: %v", tmpArchive, rmErr)
		}
	}()

	if err := os.MkdirAll(filepath.Dir(binPath), 0755); err != nil {
		return "", fmt.Errorf("failed to create directory %s: %w", filepath.Dir(binPath), err)
	}

	if runtime.GOOS == "windows" {
		if err := extractDockerZip(tmpArchive, binPath, binName); err != nil {
			return "", fmt.Errorf("failed to extract docker from %s: %w", tmpArchive, err)
		}
	} else {
		if err := extractDockerTgz(tmpArchive, binPath, binName); err != nil {
			return "", fmt.Errorf("failed to extract docker from %s: %w", tmpArchive, err)
		}
	}

	if err := os.Chmod(binPath, 0755); err != nil {
		return "", fmt.Errorf("failed to chmod docker binary %s: %w", binPath, err)
	}

	klog.Infof("Installed docker %s to %s", resolvedVer, binPath)
	return binPath, nil
}

// EnsureDockerBuildx ensures that the docker-buildx plugin binary is downloaded, verified against checksum,
// and cached in the tool cache directory. Returns the path to the executable binary.
func EnsureDockerBuildx(ctx context.Context, version string) (string, error) {
	buildxMu.Lock()
	defer buildxMu.Unlock()

	cacheRoot, err := fileutils.CacheDir()
	if err != nil {
		return "", fmt.Errorf("failed to determine cache dir: %w", err)
	}

	binName := "docker-buildx"
	if runtime.GOOS == "windows" {
		binName = "docker-buildx.exe"
	}

	normVer := strings.TrimSpace(version)
	if normVer == "" || normVer == "latest" {
		rel := LatestBuildx()
		if rel == nil {
			return "", fmt.Errorf("no buildx releases found in versions.yaml")
		}
		normVer = rel.Version
	}
	normVer = strings.TrimPrefix(normVer, "v")

	installDir := filepath.Join(cacheRoot, "tools", "docker-buildx", "v"+normVer)
	binPath := filepath.Join(installDir, "bin", binName)
	if fileutils.IsExecutable(binPath) {
		klog.V(2).Infof("Using docker-buildx v%s", normVer)
		return binPath, nil
	}

	plat, err := ResolvePlatform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}

	var expectedDigest string
	if rel, ok := LookupBuildx(normVer); ok {
		expectedDigest = rel.Assets[plat]
	} else {
		klog.Warningf("docker-buildx v%s is not in versions.yaml; downloading without verified digest", normVer)
	}

	if envDigest := os.Getenv("BUILDX_DOWNLOAD_DIGEST"); envDigest != "" {
		expectedDigest = envDigest
	}

	baseURL := os.Getenv("BUILDX_DOWNLOAD_BASE_URL")
	downloadURL, err := ResolveBuildxDownloadURL(baseURL, normVer, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}

	tmpFile, err := fetch.Download(ctx, downloadURL, expectedDigest, cacheRoot)
	if err != nil {
		return "", fmt.Errorf("failed to download docker-buildx from %s: %w", downloadURL, err)
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
		return "", fmt.Errorf("failed to install buildx binary %s: %w", binPath, err)
	}

	klog.Infof("Installed docker-buildx v%s to %s", normVer, binPath)
	return binPath, nil
}

// EnsureDockerWithBuildx ensures that both docker and docker-buildx are installed and ready.
func EnsureDockerWithBuildx(ctx context.Context, dockerVer, buildxVer string) (dockerBin, buildxBin string, err error) {
	dockerBin, err = EnsureDocker(ctx, dockerVer)
	if err != nil {
		return "", "", err
	}

	buildxBin, err = EnsureDockerBuildx(ctx, buildxVer)
	if err != nil {
		return "", "", err
	}

	return dockerBin, buildxBin, nil
}

func extractTarEntry(tr io.Reader, dstBin string) error {
	dstDir := filepath.Dir(dstBin)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp(dstDir, ".docker-*.tmp")
	if err != nil {
		return err
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

	if _, err := io.Copy(tmpFile, tr); err != nil {
		return fmt.Errorf("failed to extract from tar: %w", err)
	}
	if err := tmpFile.Chmod(0755); err != nil {
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dstBin); err != nil {
		return err
	}
	success = true
	return nil
}

func extractDockerTgz(tgzPath, dstBin, targetName string) error {
	f, err := os.Open(tgzPath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			klog.V(2).Infof("failed to close %s: %v", tgzPath, closeErr)
		}
	}()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := gzr.Close(); closeErr != nil {
			klog.V(2).Infof("failed to close gzip reader: %v", closeErr)
		}
	}()

	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		cleanName := filepath.Clean(hdr.Name)
		if hdr.Typeflag == tar.TypeReg && (cleanName == targetName || cleanName == filepath.Join("docker", targetName) || strings.HasSuffix(cleanName, targetName)) {
			return extractTarEntry(tr, dstBin)
		}
	}
	return fmt.Errorf("target binary %q not found in archive %s", targetName, tgzPath)
}

func extractZipEntry(file *zip.File, dstBin string) error {
	dstDir := filepath.Dir(dstBin)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return err
	}
	rc, err := file.Open()
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := rc.Close(); closeErr != nil {
			klog.V(2).Infof("failed to close zip entry: %v", closeErr)
		}
	}()

	tmpFile, err := os.CreateTemp(dstDir, ".docker-*.tmp")
	if err != nil {
		return err
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

	if _, err := io.Copy(tmpFile, rc); err != nil {
		return fmt.Errorf("failed to extract from zip: %w", err)
	}
	if err := tmpFile.Chmod(0755); err != nil {
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dstBin); err != nil {
		return err
	}
	success = true
	return nil
}

func extractDockerZip(zipPath, dstBin, targetName string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := zr.Close(); closeErr != nil {
			klog.V(2).Infof("failed to close %s: %v", zipPath, closeErr)
		}
	}()

	for _, file := range zr.File {
		cleanName := filepath.Clean(file.Name)
		if !file.FileInfo().IsDir() && (cleanName == targetName || cleanName == filepath.Join("docker", targetName) || strings.HasSuffix(cleanName, targetName)) {
			return extractZipEntry(file, dstBin)
		}
	}
	return fmt.Errorf("target binary %q not found in zip %s", targetName, zipPath)
}
