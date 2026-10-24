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
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
)

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func createGoArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	if runtime.GOOS == "windows" {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for name, content := range files {
			w, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(content); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0755,
			Size: int64(len(content)),
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

func TestColdRun_DoesNotMutateHome(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	binName := "go"
	if runtime.GOOS == "windows" {
		binName = "go.exe"
	}

	binContent := []byte("#!/bin/sh\necho mock go\n")
	archiveBytes := createGoArchive(t, map[string][]byte{
		"go/bin/" + binName: binContent,
	})

	hasher := sha256.New()
	hasher.Write(archiveBytes)
	digest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(archiveBytes)
	}))
	defer server.Close()

	t.Setenv("GO_DOWNLOAD_BASE_URL", server.URL)
	t.Setenv("GO_DOWNLOAD_DIGEST", digest)

	binPath, err := EnsureGo(t.Context(), "go1.27.3")
	if err != nil {
		t.Fatalf("EnsureGo failed: %v", err)
	}

	if !fileutils.IsExecutable(binPath) {
		t.Errorf("expected Go binary %s to be executable", binPath)
	}

	entries, err := os.ReadDir(tempHome)
	if err != nil {
		t.Fatalf("failed to read temp home: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected HOME (%s) to remain untouched, but found %d entries: %v", tempHome, len(entries), entries)
	}
}

func TestOfflineResolution_FailingClient(t *testing.T) {
	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	binName := "go"
	if runtime.GOOS == "windows" {
		binName = "go.exe"
	}

	binDir := filepath.Join(tempCache, "tools", "go", "go1.27.3", "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	expectedBin := filepath.Join(binDir, binName)
	if err := os.WriteFile(expectedBin, []byte("#!/bin/sh\necho go\n"), 0755); err != nil {
		t.Fatal(err)
	}

	failingClient := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("network is offline: %s", req.URL)
		}),
	}
	ctx := scoped.WithHTTPClient(t.Context(), failingClient)

	gotBin, err := EnsureGo(ctx, "go1.27.3")
	if err != nil {
		t.Fatalf("unexpected error resolving cached Go offline: %v", err)
	}
	if gotBin != expectedBin {
		t.Errorf("got bin %q, want %q", gotBin, expectedBin)
	}
}

func TestVersionResolutionOrder(t *testing.T) {
	t.Run("go.mod toolchain", func(t *testing.T) {
		dir := t.TempDir()
		goMod := "module example.com/test\n\ngo 1.27.2\n\ntoolchain go1.27.3\n"
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0644); err != nil {
			t.Fatal(err)
		}
		apDir := filepath.Join(dir, ".ap")
		if err := os.MkdirAll(apDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte("toolchain: go1.27.1\n"), 0644); err != nil {
			t.Fatal(err)
		}

		ver, err := ResolveVersion(dir, dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ver != "go1.27.3" {
			t.Errorf("got version %q, want %q", ver, "go1.27.3")
		}
	})

	t.Run("go.mod go line", func(t *testing.T) {
		dir := t.TempDir()
		goMod := "module example.com/test\n\ngo 1.27.2\n"
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0644); err != nil {
			t.Fatal(err)
		}
		apDir := filepath.Join(dir, ".ap")
		if err := os.MkdirAll(apDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte("toolchain: go1.27.1\n"), 0644); err != nil {
			t.Fatal(err)
		}

		ver, err := ResolveVersion(dir, dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ver != "go1.27.2" {
			t.Errorf("got version %q, want %q", ver, "go1.27.2")
		}
	})

	t.Run("config", func(t *testing.T) {
		dir := t.TempDir()
		apDir := filepath.Join(dir, ".ap")
		if err := os.MkdirAll(apDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte("toolchain: go1.27.1\n"), 0644); err != nil {
			t.Fatal(err)
		}

		ver, err := ResolveVersion(dir, dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ver != "go1.27.1" {
			t.Errorf("got version %q, want %q", ver, "go1.27.1")
		}
	})

	t.Run("table", func(t *testing.T) {
		dir := t.TempDir()

		ver, err := ResolveVersion(dir, dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		latest := LatestGo()
		if latest == nil {
			t.Fatal("LatestGo returned nil")
		}
		if ver != latest.Version {
			t.Errorf("got version %q, want %q", ver, latest.Version)
		}
	})
}
