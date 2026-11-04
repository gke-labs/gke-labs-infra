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

package protoc

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"k8s.io/klog/v2"
)

func TestResolveProtocAsset(t *testing.T) {
	tests := []struct {
		version string
		goos    string
		goarch  string
		want    string
		wantErr bool
	}{
		{"36.2", "linux", "amd64", "protoc-36.2-linux-x86_64.zip", false},
		{"v36.2", "linux", "arm64", "protoc-36.2-linux-aarch_64.zip", false},
		{"36.2", "darwin", "amd64", "protoc-36.2-osx-x86_64.zip", false},
		{"36.2", "darwin", "arm64", "protoc-36.2-osx-aarch_64.zip", false},
		{"36.2", "windows", "amd64", "protoc-36.2-win64.zip", false},
		{"36.2", "unknown", "amd64", "", true},
		{"36.2", "linux", "unknown", "", true},
	}

	for _, tt := range tests {
		got, err := ResolveProtocAsset(tt.version, tt.goos, tt.goarch)
		if (err != nil) != tt.wantErr {
			t.Errorf("ResolveProtocAsset(%s, %s, %s) error = %v, wantErr %v", tt.version, tt.goos, tt.goarch, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("ResolveProtocAsset(%s, %s, %s) = %s, want %s", tt.version, tt.goos, tt.goarch, got, tt.want)
		}
	}
}

type failTransport struct{}

func (f *failTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("network is disabled: %s", req.URL)
}

func TestEnsureProtoc_Resolution(t *testing.T) {
	ctx := scoped.WithHTTPClient(t.Context(), &http.Client{Transport: &failTransport{}})

	// 1. "latest" resolves to table entry (36.2) with no network
	ver, downloadURL, digest, err := resolveProtocRelease(ctx, "latest")
	if err != nil {
		t.Fatalf("resolveProtocRelease(latest) failed: %v", err)
	}
	if ver != "36.2" {
		t.Errorf("expected version 36.2, got %s", ver)
	}
	if !strings.Contains(downloadURL, "36.2") {
		t.Errorf("expected download URL to contain 36.2, got %s", downloadURL)
	}
	if !strings.HasPrefix(digest, "sha256:") {
		t.Errorf("expected digest with sha256: prefix, got %s", digest)
	}

	// 2. Listed version (35.1) resolves to table entry with no network
	ver, downloadURL, digest, err = resolveProtocRelease(ctx, "35.1")
	if err != nil {
		t.Fatalf("resolveProtocRelease(35.1) failed: %v", err)
	}
	if ver != "35.1" {
		t.Errorf("expected version 35.1, got %s", ver)
	}
	if !strings.Contains(downloadURL, "35.1") {
		t.Errorf("expected download URL to contain 35.1, got %s", downloadURL)
	}
	if !strings.HasPrefix(digest, "sha256:") {
		t.Errorf("expected digest with sha256: prefix, got %s", digest)
	}

	// 3. Unlisted version (24.4) warns and falls back to GitHub API (which fails via failing client)
	var logBuf bytes.Buffer
	klog.SetOutput(&logBuf)
	klog.LogToStderr(false)
	defer func() {
		klog.SetOutput(os.Stderr)
		klog.LogToStderr(true)
	}()

	_, _, _, err = resolveProtocRelease(ctx, "24.4")
	if err == nil {
		t.Fatal("expected error from failed network request on unlisted version, got nil")
	}
	if !strings.Contains(err.Error(), "network is disabled") {
		t.Errorf("expected error to mention disabled network, got: %v", err)
	}

	wantWarn := "protoc 24.4 is not in versions.yaml; resolving it from the GitHub API, which is rate limited; run 'ap tool update-versions' to add it"
	if !strings.Contains(logBuf.String(), wantWarn) {
		t.Errorf("expected warning %q in log output, got: %s", wantWarn, logBuf.String())
	}
}

func TestEnsureProtoc_Cached(t *testing.T) {
	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	version := "36.2"
	binDir := filepath.Join(tempCache, "tools", "protoc", version, "bin")
	includeDir := filepath.Join(tempCache, "tools", "protoc", version, "include")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(includeDir, 0755); err != nil {
		t.Fatal(err)
	}

	fakeBin := filepath.Join(binDir, "protoc")
	if runtime.GOOS == "windows" {
		fakeBin += ".exe"
	}
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\necho fake\n"), 0755); err != nil {
		t.Fatal(err)
	}

	var logBuf bytes.Buffer
	klog.SetOutput(&logBuf)
	klog.LogToStderr(false)
	defer func() {
		klog.SetOutput(os.Stderr)
		klog.LogToStderr(true)
	}()

	bin, inc, err := EnsureProtoc(t.Context(), version)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bin != fakeBin {
		t.Errorf("expected bin %s, got %s", fakeBin, bin)
	}
	if inc != includeDir {
		t.Errorf("expected inc %s, got %s", includeDir, inc)
	}
	if logBuf.Len() > 0 {
		t.Errorf("expected no log output at default verbosity when cached, got: %q", logBuf.String())
	}
}

func TestEnsureProtoc_ChecksumMismatch(t *testing.T) {
	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("corrupt zip file"))
	}))
	defer server.Close()

	t.Setenv("PROTOC_DOWNLOAD_BASE_URL", server.URL)
	t.Setenv("PROTOC_DOWNLOAD_DIGEST", "sha256:0000000000000000000000000000000000000000000000000000000000000000")

	_, _, err := EnsureProtoc(t.Context(), "36.2")
	if err == nil {
		t.Fatal("expected error on checksum mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("expected error to mention checksum mismatch, got: %v", err)
	}
}

func TestDetectRequiredPlugins(t *testing.T) {
	args := []string{
		"-I", "proto",
		"--go_out=paths=source_relative:.",
		"--go_opt=module=github.com/example",
		"--go-grpc_out=.",
		"--python_out=.",
		"--plugin=protoc-gen-custom=/usr/bin/custom",
		"--plugin=protoc-gen-extra",
		"proto/foo.proto",
	}

	got := DetectRequiredPlugins(args)
	want := []string{"protoc-gen-extra", "protoc-gen-go", "protoc-gen-go-grpc"}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("DetectRequiredPlugins() = %v, want %v", got, want)
	}
}

func TestEnsurePlugin_Unknown(t *testing.T) {
	_, err := EnsurePlugin(t.Context(), "protoc-gen-unknown", "latest", nil)
	if err == nil {
		t.Fatal("expected error for unknown plugin, got nil")
	}
	if !strings.Contains(err.Error(), "unknown protoc plugin") {
		t.Errorf("expected error to mention unknown protoc plugin, got: %v", err)
	}
}

func TestEnsurePlugin_Cached(t *testing.T) {
	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	pluginName := "protoc-gen-go"
	version := "v1.36.12"
	binDir := filepath.Join(tempCache, "tools", "plugins", pluginName, version, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}

	fakeBin := filepath.Join(binDir, pluginName)
	if runtime.GOOS == "windows" {
		fakeBin += ".exe"
	}
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\necho fake\n"), 0755); err != nil {
		t.Fatal(err)
	}

	var logBuf bytes.Buffer
	klog.SetOutput(&logBuf)
	klog.LogToStderr(false)
	defer func() {
		klog.SetOutput(os.Stderr)
		klog.LogToStderr(true)
	}()

	dir, err := EnsurePlugin(t.Context(), pluginName, version, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dir != binDir {
		t.Errorf("expected binDir %s, got %s", binDir, dir)
	}
	if logBuf.Len() > 0 {
		t.Errorf("expected no log output at default verbosity when cached, got: %q", logBuf.String())
	}
}

func TestEnsureProtoc_RealDownloadAndExecute(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real download and execution in short mode")
	}

	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	ctx := t.Context()
	protocBin, includeDir, err := EnsureProtoc(ctx, "36.2")
	if err != nil {
		t.Fatalf("failed to ensure protoc: %v", err)
	}

	if fi, err := os.Stat(protocBin); err != nil || fi.IsDir() {
		t.Fatalf("protoc binary not found at %s: %v", protocBin, err)
	}
	if fi, err := os.Stat(includeDir); err != nil || !fi.IsDir() {
		t.Fatalf("include dir not found at %s: %v", includeDir, err)
	}

	cmd := exec.CommandContext(ctx, protocBin, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to run protoc --version: %v\nOutput: %s", err, string(out))
	}
	if !strings.Contains(string(out), "libprotoc") && !strings.Contains(string(out), "protoc") {
		t.Errorf("unexpected output from protoc --version: %s", string(out))
	}
}

func TestFetchGitHubRelease_AuthorizationHeader(t *testing.T) {
	var gotAuthHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{"tag_name": "v36.2", "assets": []}`)); err != nil {
			return
		}
	}))
	defer server.Close()

	ctx := scoped.WithHTTPClient(t.Context(), server.Client())

	t.Setenv("GITHUB_TOKEN", "test-bearer-token")
	_, err := fetchGitHubRelease(ctx, server.URL)
	if err != nil {
		t.Fatalf("fetchGitHubRelease failed: %v", err)
	}

	wantAuth := "Bearer test-bearer-token"
	if gotAuthHeader != wantAuth {
		t.Errorf("got Authorization %q, want %q", gotAuthHeader, wantAuth)
	}
}
