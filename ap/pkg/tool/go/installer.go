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

package gotool

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

var goMu sync.Mutex

// ResolveGoDownloadURL computes the download URL for official Go release archives.
func ResolveGoDownloadURL(baseURL, version, goos, goarch string) (string, error) {
	plat, err := ResolvePlatform(goos, goarch)
	if err != nil {
		return "", err
	}

	normVer := NormalizeVersion(version)
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}

	if baseURL == "" {
		baseURL = "https://go.dev/dl"
	}

	filename := fmt.Sprintf("%s.%s%s", normVer, plat, ext)
	return fmt.Sprintf("%s/%s", strings.TrimRight(baseURL, "/"), filename), nil
}

// EnsureGo ensures that the Go toolchain for the specified version is downloaded,
// verified against its checksum, and cached in AP_CACHE_DIR/tools/go/<version>.
// Returns the path to the go executable.
func EnsureGo(ctx context.Context, version string) (string, error) {
	goMu.Lock()
	defer goMu.Unlock()

	cacheRoot, err := fileutils.CacheDir()
	if err != nil {
		return "", fmt.Errorf("failed to determine cache dir: %w", err)
	}

	binName := "go"
	if runtime.GOOS == "windows" {
		binName = "go.exe"
	}

	resolvedVer := NormalizeVersion(version)
	if resolvedVer == "" || resolvedVer == "latest" {
		rel := LatestGo()
		if rel == nil {
			return "", fmt.Errorf("no Go releases found in versions.yaml")
		}
		resolvedVer = rel.Version
	}

	toolsDir := filepath.Join(cacheRoot, "tools", "go")
	installDir := filepath.Join(toolsDir, resolvedVer)
	binPath := filepath.Join(installDir, "bin", binName)

	if fileutils.IsExecutable(binPath) {
		klog.Infof("Using Go %s from %s", resolvedVer, binPath)
		return binPath, nil
	}

	plat, err := ResolvePlatform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}

	var expectedDigest string
	if rel, ok := LookupGo(resolvedVer); ok {
		expectedDigest = rel.Assets[plat]
	} else {
		klog.Warningf("Go %s is not in versions.yaml; downloading without verified digest", resolvedVer)
	}

	if envDigest := os.Getenv("GO_DOWNLOAD_DIGEST"); envDigest != "" {
		expectedDigest = envDigest
	}

	baseURL := os.Getenv("GO_DOWNLOAD_BASE_URL")
	downloadURL, err := ResolveGoDownloadURL(baseURL, resolvedVer, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}

	tmpArchive, err := fetch.Download(ctx, downloadURL, expectedDigest, cacheRoot)
	if err != nil {
		return "", fmt.Errorf("failed to download Go from %s: %w", downloadURL, err)
	}
	defer func() {
		if rmErr := os.Remove(tmpArchive); rmErr != nil && !os.IsNotExist(rmErr) {
			klog.V(2).Infof("failed to remove temporary archive %s: %v", tmpArchive, rmErr)
		}
	}()

	if err := os.MkdirAll(toolsDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory %s: %w", toolsDir, err)
	}

	tmpInstallDir, err := os.MkdirTemp(toolsDir, ".go-*.tmp")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary extraction directory: %w", err)
	}
	var success bool
	defer func() {
		if !success {
			if rmErr := os.RemoveAll(tmpInstallDir); rmErr != nil && !os.IsNotExist(rmErr) {
				klog.V(2).Infof("failed to remove temporary extraction directory %s: %v", tmpInstallDir, rmErr)
			}
		}
	}()

	if runtime.GOOS == "windows" {
		if err := extractGoZip(tmpArchive, tmpInstallDir); err != nil {
			return "", fmt.Errorf("failed to extract Go from %s: %w", tmpArchive, err)
		}
	} else {
		if err := extractGoTgz(tmpArchive, tmpInstallDir); err != nil {
			return "", fmt.Errorf("failed to extract Go from %s: %w", tmpArchive, err)
		}
	}

	tmpBinPath := filepath.Join(tmpInstallDir, "bin", binName)
	if err := os.Chmod(tmpBinPath, 0755); err != nil && !os.IsNotExist(err) {
		klog.V(2).Infof("chmod on %s: %v", tmpBinPath, err)
	}

	if err := os.Rename(tmpInstallDir, installDir); err != nil {
		if fileutils.IsExecutable(binPath) {
			if rmErr := os.RemoveAll(tmpInstallDir); rmErr != nil && !os.IsNotExist(rmErr) {
				klog.V(2).Infof("failed to remove temporary extraction directory %s: %v", tmpInstallDir, rmErr)
			}
			return binPath, nil
		}
		return "", fmt.Errorf("failed to install Go to %s: %w", installDir, err)
	}
	success = true

	klog.Infof("Installed Go %s to %s", resolvedVer, binPath)
	return binPath, nil
}

// EnsureGoForRepo resolves the Go toolchain version for the given directory/repo and ensures it is installed.
func EnsureGoForRepo(ctx context.Context, dir string) (string, error) {
	version, err := ResolveVersion(dir, dir)
	if err != nil {
		return "", err
	}
	return EnsureGo(ctx, version)
}

func extractGoTgz(tgzPath, dstDir string) error {
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

		cleanName := filepath.ToSlash(filepath.Clean(hdr.Name))
		subPath := strings.TrimPrefix(cleanName, "go/")
		if subPath == "" || subPath == "go" || subPath == "." {
			continue
		}

		target := filepath.Join(dstDir, filepath.FromSlash(subPath))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			if rmErr := os.Remove(target); rmErr != nil && !os.IsNotExist(rmErr) {
				klog.V(2).Infof("failed to remove existing target %s: %v", target, rmErr)
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			perm := hdr.FileInfo().Mode().Perm()
			if perm == 0 {
				perm = 0644
			}
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(outFile, tr)
			closeErr := outFile.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
	return nil
}

func extractGoZip(zipPath, dstDir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := zr.Close(); closeErr != nil {
			klog.V(2).Infof("failed to close zip %s: %v", zipPath, closeErr)
		}
	}()

	for _, file := range zr.File {
		cleanName := filepath.ToSlash(filepath.Clean(file.Name))
		subPath := strings.TrimPrefix(cleanName, "go/")
		if subPath == "" || subPath == "go" || subPath == "." {
			continue
		}

		target := filepath.Join(dstDir, filepath.FromSlash(subPath))
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}

		if err := extractZipFile(file, target); err != nil {
			return err
		}
	}
	return nil
}

func extractZipFile(file *zip.File, target string) error {
	rc, err := file.Open()
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := rc.Close(); closeErr != nil {
			klog.V(2).Infof("failed to close zip file entry %s: %v", file.Name, closeErr)
		}
	}()

	perm := file.Mode().Perm()
	if perm == 0 {
		perm = 0644
	}

	outFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}

	_, copyErr := io.Copy(outFile, rc)
	closeErr := outFile.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}
