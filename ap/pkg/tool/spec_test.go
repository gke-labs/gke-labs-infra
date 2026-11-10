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

package tool

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
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/kind"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/spec"
	toolversions "github.com/gke-labs/gke-labs-infra/ap/pkg/tool/versions"
	"k8s.io/klog/v2"
)

type failTransport struct{}

func (f *failTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("network is disabled: %s", req.URL)
}

func TestSpec_Resolution_Offline(t *testing.T) {
	ctx := scoped.WithHTTPClient(t.Context(), &http.Client{Transport: &failTransport{}})

	// 1. "latest" resolves to table entry (v0.33.0) with no network
	rel, err := spec.ResolveRelease(ctx, kind.Spec, "latest")
	if err != nil {
		t.Fatalf("ResolveRelease(latest) failed: %v", err)
	}
	if rel.Version != "v0.33.0" {
		t.Errorf("expected version v0.33.0, got %s", rel.Version)
	}
	if !strings.Contains(rel.URL, "v0.33.0") {
		t.Errorf("expected download URL to contain v0.33.0, got %s", rel.URL)
	}
	if !strings.HasPrefix(rel.Digest, "sha256:") {
		t.Errorf("expected digest with sha256: prefix, got %s", rel.Digest)
	}

	// 2. Listed version (v0.32.0) resolves to table entry with no network
	rel, err = spec.ResolveRelease(ctx, kind.Spec, "v0.32.0")
	if err != nil {
		t.Fatalf("ResolveRelease(v0.32.0) failed: %v", err)
	}
	if rel.Version != "v0.32.0" {
		t.Errorf("expected version v0.32.0, got %s", rel.Version)
	}
	if !strings.Contains(rel.URL, "v0.32.0") {
		t.Errorf("expected download URL to contain v0.32.0, got %s", rel.URL)
	}
	if !strings.HasPrefix(rel.Digest, "sha256:") {
		t.Errorf("expected digest with sha256: prefix, got %s", rel.Digest)
	}

	// 3. Unlisted version (v0.20.0) warns and falls back to GitHub API (which fails via failing client)
	var logBuf bytes.Buffer
	klog.SetOutput(&logBuf)
	klog.LogToStderr(false)
	defer func() {
		klog.SetOutput(os.Stderr)
		klog.LogToStderr(true)
	}()

	_, err = spec.ResolveRelease(ctx, kind.Spec, "v0.20.0")
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

func TestSpec_Cached(t *testing.T) {
	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	binName := kind.Spec.BinaryName()
	binDir := filepath.Join(tempCache, "tools", kind.Spec.Name, "v0.33.0", "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	expectedBin := filepath.Join(binDir, binName)
	if err := os.WriteFile(expectedBin, []byte("#!/bin/sh\necho kind\n"), 0755); err != nil {
		t.Fatal(err)
	}

	ctx := scoped.WithHTTPClient(t.Context(), &http.Client{Transport: &failTransport{}})

	var logBuf bytes.Buffer
	klog.SetOutput(&logBuf)
	klog.LogToStderr(false)
	defer func() {
		klog.SetOutput(os.Stderr)
		klog.LogToStderr(true)
	}()

	gotBin, err := spec.Ensure(ctx, kind.Spec, "v0.33.0")
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

func TestSpec_DownloadAndVerify(t *testing.T) {
	binContent := []byte("#!/bin/sh\necho mock binary\n")
	hasher := sha256.New()
	hasher.Write(binContent)
	digest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(binContent)
	}))
	defer server.Close()

	plat := runtime.GOOS + "-" + runtime.GOARCH
	testYAML := fmt.Sprintf(`kind:
  - version: v0.33.0
    assets:
      %s: %s
`, plat, digest)

	testSpec := &spec.Spec{
		Name:      "kind",
		Platforms: []string{plat},
		Source: &spec.GitHubReleases{
			Repo:    "kubernetes-sigs/kind",
			BaseURL: server.URL,
			Asset: func(tag, goos, goarch string) string {
				return fmt.Sprintf("kind-%s-%s", goos, goarch)
			},
		},
		Archive:      spec.Bare{},
		VersionsYAML: []byte(testYAML),
	}

	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	binPath, err := spec.Ensure(t.Context(), testSpec, "v0.33.0")
	if err != nil {
		t.Fatalf("Ensure failed: %v", err)
	}

	data, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("reading installed binary: %v", err)
	}
	if string(data) != string(binContent) {
		t.Errorf("binary content mismatch: got %q, want %q", string(data), string(binContent))
	}

	if !fileutils.IsExecutable(binPath) {
		t.Errorf("expected binary %s to be executable", binPath)
	}
}

func TestSpec_ChecksumMismatch(t *testing.T) {
	binContent := []byte("#!/bin/sh\necho bad\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(binContent)
	}))
	defer server.Close()

	plat := runtime.GOOS + "-" + runtime.GOARCH
	testYAML := fmt.Sprintf(`kind:
  - version: v0.33.0
    assets:
      %s: sha256:0000000000000000000000000000000000000000000000000000000000000000
`, plat)

	testSpec := &spec.Spec{
		Name:      "kind",
		Platforms: []string{plat},
		Source: &spec.GitHubReleases{
			Repo:    "kubernetes-sigs/kind",
			BaseURL: server.URL,
			Asset: func(tag, goos, goarch string) string {
				return fmt.Sprintf("kind-%s-%s", goos, goarch)
			},
		},
		Archive:      spec.Bare{},
		VersionsYAML: []byte(testYAML),
	}

	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	_, err := spec.Ensure(t.Context(), testSpec, "v0.33.0")
	if err == nil {
		t.Fatal("expected checksum mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("expected error to mention checksum mismatch, got %v", err)
	}
}

func TestSpec_ColdRun_DoesNotMutateHome(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	binContent := []byte("#!/bin/sh\necho mock binary\n")
	hasher := sha256.New()
	hasher.Write(binContent)
	digest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(binContent)
	}))
	defer server.Close()

	plat := runtime.GOOS + "-" + runtime.GOARCH
	testYAML := fmt.Sprintf(`kind:
  - version: v0.33.0
    assets:
      %s: %s
`, plat, digest)

	testSpec := &spec.Spec{
		Name:      "kind",
		Platforms: []string{plat},
		Source: &spec.GitHubReleases{
			Repo:    "kubernetes-sigs/kind",
			BaseURL: server.URL,
			Asset: func(tag, goos, goarch string) string {
				return fmt.Sprintf("kind-%s-%s", goos, goarch)
			},
		},
		Archive:      spec.Bare{},
		VersionsYAML: []byte(testYAML),
	}

	binPath, err := spec.Ensure(t.Context(), testSpec, "v0.33.0")
	if err != nil {
		t.Fatalf("Ensure failed: %v", err)
	}
	if !fileutils.IsExecutable(binPath) {
		t.Errorf("expected binary %s to be executable", binPath)
	}

	entries, err := os.ReadDir(tempHome)
	if err != nil {
		t.Fatalf("failed to read temp home: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected HOME (%s) to remain untouched, but found %d entries: %v", tempHome, len(entries), entries)
	}
}

func TestSpec_VersionsYAML(t *testing.T) {
	rels, err := toolversions.Parse(kind.Spec.VersionsYAML, kind.Spec.Name)
	if err != nil {
		t.Fatalf("failed to parse versions.yaml: %v", err)
	}
	if len(rels) != 5 {
		t.Fatalf("expected exactly 5 releases in versions.yaml, got %d", len(rels))
	}

	for _, rel := range rels {
		if rel.Version == "" {
			t.Errorf("empty version in release")
		}
		if len(rel.Assets) != len(kind.Spec.Platforms) {
			t.Errorf("release %s has %d assets, want %d", rel.Version, len(rel.Assets), len(kind.Spec.Platforms))
		}
		for _, plat := range kind.Spec.Platforms {
			digest, ok := rel.Assets[plat]
			if !ok || digest == "" {
				t.Errorf("release %s missing asset for platform %s", rel.Version, plat)
				continue
			}
			if !strings.HasPrefix(digest, "sha256:") {
				t.Errorf("release %s asset %s digest does not have sha256: prefix: %s", rel.Version, plat, digest)
			}
			if len(digest) != len("sha256:")+64 {
				t.Errorf("release %s asset %s digest length is invalid: %s", rel.Version, plat, digest)
			}
		}
	}

	var prev string
	for i, rel := range rels {
		if i > 0 {
			if toolversions.Compare(prev, rel.Version) <= 0 {
				t.Errorf("versions table is not sorted newest-first: %s is not newer than %s", prev, rel.Version)
			}
		}
		prev = rel.Version
	}
}

func TestSpec_ConfigLoad(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		tmpDir := t.TempDir()
		ver, err := spec.LoadConfig(kind.Spec, tmpDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ver != "" {
			t.Fatalf("expected empty version for missing file, got %q", ver)
		}
	})

	t.Run("valid", func(t *testing.T) {
		tmpDir := t.TempDir()
		apDir := filepath.Join(tmpDir, ".ap")
		if err := os.MkdirAll(apDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(apDir, "kind.yaml"), []byte("kind: \"v0.33.0\"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		ver, err := spec.LoadConfig(kind.Spec, tmpDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ver != "v0.33.0" {
			t.Errorf("expected version v0.33.0, got %q", ver)
		}
	})

	t.Run("unknown key", func(t *testing.T) {
		tmpDir := t.TempDir()
		apDir := filepath.Join(tmpDir, ".ap")
		if err := os.MkdirAll(apDir, 0755); err != nil {
			t.Fatal(err)
		}
		content := "kind: \"v0.33.0\"\nunknown_field: \"invalid\"\n"
		if err := os.WriteFile(filepath.Join(apDir, "kind.yaml"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		_, err := spec.LoadConfig(kind.Spec, tmpDir)
		if err == nil {
			t.Fatal("expected error parsing kind.yaml with unknown field, got nil")
		}
	})

	t.Run("defaults", func(t *testing.T) {
		tmpDir := t.TempDir()
		apDir := filepath.Join(tmpDir, ".ap")
		if err := os.MkdirAll(apDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(apDir, "kind.yaml"), []byte("{}\n"), 0644); err != nil {
			t.Fatal(err)
		}

		ver, err := spec.LoadConfig(kind.Spec, tmpDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expectedVer := kind.Spec.LatestRelease().Version
		if ver != expectedVer {
			t.Errorf("expected default version %q, got %q", expectedVer, ver)
		}
	})
}
