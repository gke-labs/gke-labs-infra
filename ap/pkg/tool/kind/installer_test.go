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
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
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

func TestResolveKindAsset(t *testing.T) {
	got, err := ResolveKindAsset("linux", "amd64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "kind-linux-amd64" {
		t.Errorf("got %q, want %q", got, "kind-linux-amd64")
	}

	gotWin, err := ResolveKindAsset("windows", "amd64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotWin != "kind-windows-amd64" {
		t.Errorf("got %q, want %q", gotWin, "kind-windows-amd64")
	}
}

func TestResolveKindDownloadURL(t *testing.T) {
	url, err := ResolveKindDownloadURL("", "v0.33.0", "linux", "amd64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://github.com/kubernetes-sigs/kind/releases/download/v0.33.0/kind-linux-amd64"
	if url != want {
		t.Errorf("got %q, want %q", url, want)
	}
}

type failTransport struct{}

func (f *failTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("network is disabled: %s", req.URL)
}

func TestEnsureKind_Resolution_Offline(t *testing.T) {
	ctx := scoped.WithHTTPClient(t.Context(), &http.Client{Transport: &failTransport{}})

	// 1. "latest" resolves to table entry (v0.33.0) with no network
	ver, downloadURL, digest, err := resolveKindRelease(ctx, "latest")
	if err != nil {
		t.Fatalf("resolveKindRelease(latest) failed: %v", err)
	}
	if ver != "v0.33.0" {
		t.Errorf("expected version v0.33.0, got %s", ver)
	}
	if !strings.Contains(downloadURL, "v0.33.0") {
		t.Errorf("expected download URL to contain v0.33.0, got %s", downloadURL)
	}
	if !strings.HasPrefix(digest, "sha256:") {
		t.Errorf("expected digest with sha256: prefix, got %s", digest)
	}

	// 2. Listed version (v0.32.0) resolves to table entry with no network
	ver, downloadURL, digest, err = resolveKindRelease(ctx, "v0.32.0")
	if err != nil {
		t.Fatalf("resolveKindRelease(v0.32.0) failed: %v", err)
	}
	if ver != "v0.32.0" {
		t.Errorf("expected version v0.32.0, got %s", ver)
	}
	if !strings.Contains(downloadURL, "v0.32.0") {
		t.Errorf("expected download URL to contain v0.32.0, got %s", downloadURL)
	}
	if !strings.HasPrefix(digest, "sha256:") {
		t.Errorf("expected digest with sha256: prefix, got %s", digest)
	}

	// 3. Unlisted version (v0.20.0) warns and falls back to GitHub API (which fails via failing client)
	var logBuf bytes.Buffer
	klog.SetOutput(&logBuf)
	klog.LogToStderr(false)
	defer func() {
		klog.SetOutput(os.Stderr)
		klog.LogToStderr(true)
	}()

	_, _, _, err = resolveKindRelease(ctx, "v0.20.0")
	if err == nil {
		t.Fatal("expected error from failed network request on unlisted version, got nil")
	}
	if !strings.Contains(err.Error(), "network is disabled") {
		t.Errorf("expected error to mention disabled network, got: %v", err)
	}

	wantWarn := "kind v0.20.0 is not in versions.yaml; resolving it from the GitHub API, which is rate limited; run 'ap tool update-versions' to add it"
	if !strings.Contains(logBuf.String(), wantWarn) {
		t.Errorf("expected warning %q in log output, got: %s", wantWarn, logBuf.String())
	}
}

func TestEnsureKind_Cached(t *testing.T) {
	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	binName := "kind"
	if runtime.GOOS == "windows" {
		binName = "kind.exe"
	}
	binDir := filepath.Join(tempCache, "tools", "kind", "v0.33.0", "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	expectedBin := filepath.Join(binDir, binName)
	if err := os.WriteFile(expectedBin, []byte("#!/bin/sh\necho kind\n"), 0755); err != nil {
		t.Fatal(err)
	}

	var logBuf bytes.Buffer
	klog.SetOutput(&logBuf)
	klog.LogToStderr(false)
	defer func() {
		klog.SetOutput(os.Stderr)
		klog.LogToStderr(true)
	}()

	gotBin, err := EnsureKind(t.Context(), "v0.33.0")
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

func TestEnsureKind_DownloadAndVerify(t *testing.T) {
	binContent := []byte("#!/bin/sh\necho mock kind\n")
	hasher := sha256.New()
	hasher.Write(binContent)
	digest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(binContent)
	}))
	defer server.Close()

	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)
	t.Setenv("KIND_DOWNLOAD_BASE_URL", server.URL)
	t.Setenv("KIND_DOWNLOAD_DIGEST", digest)

	binPath, err := EnsureKind(t.Context(), "v0.33.0")
	if err != nil {
		t.Fatalf("EnsureKind failed: %v", err)
	}

	data, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("reading installed kind binary: %v", err)
	}
	if string(data) != string(binContent) {
		t.Errorf("binary content mismatch: got %q, want %q", string(data), string(binContent))
	}

	if !fileutils.IsExecutable(binPath) {
		t.Errorf("expected binary %s to be executable", binPath)
	}
}

func TestEnsureKind_ChecksumMismatch(t *testing.T) {
	binContent := []byte("#!/bin/sh\necho bad\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(binContent)
	}))
	defer server.Close()

	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)
	t.Setenv("KIND_DOWNLOAD_BASE_URL", server.URL)
	t.Setenv("KIND_DOWNLOAD_DIGEST", "sha256:0000000000000000000000000000000000000000000000000000000000000000")

	_, err := EnsureKind(t.Context(), "v0.33.0")
	if err == nil {
		t.Fatal("expected checksum mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("expected error to mention checksum mismatch, got %v", err)
	}
}

func TestColdRun_DoesNotMutateHome(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	binContent := []byte("#!/bin/sh\necho mock kind\n")
	hasher := sha256.New()
	hasher.Write(binContent)
	digest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(binContent)
	}))
	defer server.Close()

	t.Setenv("KIND_DOWNLOAD_BASE_URL", server.URL)
	t.Setenv("KIND_DOWNLOAD_DIGEST", digest)

	binPath, err := EnsureKind(t.Context(), "v0.33.0")
	if err != nil {
		t.Fatalf("EnsureKind failed: %v", err)
	}
	if !fileutils.IsExecutable(binPath) {
		t.Errorf("expected kind %s to be executable", binPath)
	}

	entries, err := os.ReadDir(tempHome)
	if err != nil {
		t.Fatalf("failed to read temp home: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected HOME (%s) to remain untouched, but found %d entries: %v", tempHome, len(entries), entries)
	}
}
