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
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"k8s.io/klog/v2"
)

func TestResolvePlatform(t *testing.T) {
	tests := []struct {
		goos    string
		goarch  string
		want    string
		wantErr bool
	}{
		{"linux", "amd64", "linux-amd64", false},
		{"linux", "arm64", "linux-arm64", false},
		{"darwin", "amd64", "darwin-amd64", false},
		{"darwin", "arm64", "darwin-arm64", false},
		{"windows", "amd64", "windows-amd64", false},
		{"linux", "mips", "", true},
		{"freebsd", "amd64", "", true},
	}

	for _, tt := range tests {
		got, err := ResolvePlatform(tt.goos, tt.goarch)
		if (err != nil) != tt.wantErr {
			t.Errorf("ResolvePlatform(%q, %q) error = %v, wantErr %v", tt.goos, tt.goarch, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("ResolvePlatform(%q, %q) = %q, want %q", tt.goos, tt.goarch, got, tt.want)
		}
	}
}

func TestResolveDockerDownloadURL(t *testing.T) {
	url, err := ResolveDockerDownloadURL("", "28.0.1", "linux", "amd64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://download.docker.com/linux/static/stable/x86_64/docker-28.0.1.tgz"
	if url != want {
		t.Errorf("got %q, want %q", url, want)
	}

	urlMac, err := ResolveDockerDownloadURL("", "28.0.1", "darwin", "arm64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantMac := "https://download.docker.com/mac/static/stable/aarch64/docker-28.0.1.tgz"
	if urlMac != wantMac {
		t.Errorf("got %q, want %q", urlMac, wantMac)
	}

	urlWin, err := ResolveDockerDownloadURL("", "28.0.1", "windows", "amd64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantWin := "https://download.docker.com/win/static/stable/x86_64/docker-28.0.1.zip"
	if urlWin != wantWin {
		t.Errorf("got %q, want %q", urlWin, wantWin)
	}
}

func TestResolveBuildxDownloadURL(t *testing.T) {
	url, err := ResolveBuildxDownloadURL("", "v0.21.1", "linux", "amd64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://github.com/docker/buildx/releases/download/v0.21.1/buildx-v0.21.1.linux-amd64"
	if url != want {
		t.Errorf("got %q, want %q", url, want)
	}
}

func TestEnsureDocker_Cached(t *testing.T) {
	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	binName := "docker"
	if runtime.GOOS == "windows" {
		binName = "docker.exe"
	}
	binDir := filepath.Join(tempCache, "tools", "docker", "28.0.1", "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	expectedBin := filepath.Join(binDir, binName)
	if err := os.WriteFile(expectedBin, []byte("#!/bin/sh\necho docker\n"), 0755); err != nil {
		t.Fatal(err)
	}

	var logBuf bytes.Buffer
	klog.SetOutput(&logBuf)
	klog.LogToStderr(false)
	defer func() {
		klog.SetOutput(os.Stderr)
		klog.LogToStderr(true)
	}()

	gotBin, err := EnsureDocker(t.Context(), "28.0.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBin != expectedBin {
		t.Errorf("got bin %q, want %q", gotBin, expectedBin)
	}
	if logBuf.Len() > 0 {
		t.Errorf("expected no log output at default verbosity when cached, got: %q", logBuf.String())
	}
}

func TestEnsureDockerBuildx_Cached(t *testing.T) {
	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	binName := "docker-buildx"
	if runtime.GOOS == "windows" {
		binName = "docker-buildx.exe"
	}
	binDir := filepath.Join(tempCache, "tools", "docker-buildx", "v0.21.1", "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	expectedBin := filepath.Join(binDir, binName)
	if err := os.WriteFile(expectedBin, []byte("#!/bin/sh\necho buildx\n"), 0755); err != nil {
		t.Fatal(err)
	}

	var logBuf bytes.Buffer
	klog.SetOutput(&logBuf)
	klog.LogToStderr(false)
	defer func() {
		klog.SetOutput(os.Stderr)
		klog.LogToStderr(true)
	}()

	gotBin, err := EnsureDockerBuildx(t.Context(), "v0.21.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBin != expectedBin {
		t.Errorf("got bin %q, want %q", gotBin, expectedBin)
	}
	if logBuf.Len() > 0 {
		t.Errorf("expected no log output at default verbosity when cached, got: %q", logBuf.String())
	}
}

func createTarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for name, content := range files {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0755,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestEnsureDocker_DownloadAndExtract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping tar.gz test on windows")
	}

	binContent := []byte("#!/bin/sh\necho mock docker\n")
	archiveBytes := createTarGz(t, map[string][]byte{
		"docker/docker": binContent,
	})

	hasher := sha256.New()
	hasher.Write(archiveBytes)
	digest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(archiveBytes)
	}))
	defer server.Close()

	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)
	t.Setenv("DOCKER_DOWNLOAD_BASE_URL", server.URL)
	t.Setenv("DOCKER_DOWNLOAD_DIGEST", digest)

	binPath, err := EnsureDocker(t.Context(), "28.0.1")
	if err != nil {
		t.Fatalf("EnsureDocker failed: %v", err)
	}

	data, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("reading extracted docker binary: %v", err)
	}
	if string(data) != string(binContent) {
		t.Errorf("binary content mismatch: got %q, want %q", string(data), string(binContent))
	}

	if !fileutils.IsExecutable(binPath) {
		t.Errorf("expected binary %s to be executable", binPath)
	}
}

func TestEnsureDocker_ChecksumMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	archiveBytes := createTarGz(t, map[string][]byte{
		"docker/docker": []byte("#!/bin/sh\necho bad\n"),
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archiveBytes)
	}))
	defer server.Close()

	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)
	t.Setenv("DOCKER_DOWNLOAD_BASE_URL", server.URL)
	t.Setenv("DOCKER_DOWNLOAD_DIGEST", "sha256:0000000000000000000000000000000000000000000000000000000000000000")

	_, err := EnsureDocker(t.Context(), "28.0.1")
	if err == nil {
		t.Fatal("expected checksum mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("expected error to mention checksum mismatch, got %v", err)
	}
}

func TestEnsureDockerBuildx_Download(t *testing.T) {
	binContent := []byte("#!/bin/sh\necho mock buildx\n")
	hasher := sha256.New()
	hasher.Write(binContent)
	digest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(binContent)
	}))
	defer server.Close()

	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)
	t.Setenv("BUILDX_DOWNLOAD_BASE_URL", server.URL)
	t.Setenv("BUILDX_DOWNLOAD_DIGEST", digest)

	binPath, err := EnsureDockerBuildx(t.Context(), "v0.21.1")
	if err != nil {
		t.Fatalf("EnsureDockerBuildx failed: %v", err)
	}

	data, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("reading installed buildx binary: %v", err)
	}
	if string(data) != string(binContent) {
		t.Errorf("binary content mismatch: got %q, want %q", string(data), string(binContent))
	}
}

func TestColdRun_DoesNotMutateHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping tar.gz test on windows")
	}

	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("DOCKER_CONFIG", "")

	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	dockerArchive := createTarGz(t, map[string][]byte{
		"docker/docker": []byte("#!/bin/sh\necho docker\n"),
	})
	dockerHasher := sha256.New()
	dockerHasher.Write(dockerArchive)
	dockerDigest := "sha256:" + hex.EncodeToString(dockerHasher.Sum(nil))

	buildxBin := []byte("#!/bin/sh\necho buildx\n")
	buildxHasher := sha256.New()
	buildxHasher.Write(buildxBin)
	buildxDigest := "sha256:" + hex.EncodeToString(buildxHasher.Sum(nil))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "docker-") {
			_, _ = w.Write(dockerArchive)
			return
		}
		_, _ = w.Write(buildxBin)
	}))
	defer server.Close()

	t.Setenv("DOCKER_DOWNLOAD_BASE_URL", server.URL)
	t.Setenv("DOCKER_DOWNLOAD_DIGEST", dockerDigest)
	t.Setenv("BUILDX_DOWNLOAD_BASE_URL", server.URL)
	t.Setenv("BUILDX_DOWNLOAD_DIGEST", buildxDigest)

	dockerPath, buildxPath, err := EnsureDockerWithBuildx(t.Context(), "28.0.1", "v0.21.1")
	if err != nil {
		t.Fatalf("EnsureDockerWithBuildx failed: %v", err)
	}
	if !fileutils.IsExecutable(dockerPath) {
		t.Errorf("expected docker %s to be executable", dockerPath)
	}
	if !fileutils.IsExecutable(buildxPath) {
		t.Errorf("expected buildx %s to be executable", buildxPath)
	}

	entries, err := os.ReadDir(tempHome)
	if err != nil {
		t.Fatalf("failed to read temp home: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected HOME (%s) to remain untouched, but found %d entries: %v", tempHome, len(entries), entries)
	}
}
