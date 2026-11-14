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
	"archive/tar"
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
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/kind"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/kubectl"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/kustomize"
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
	for _, s := range []*spec.Spec{kind.Spec, kubectl.Spec, kustomize.Spec} {
		t.Run(s.Name, func(t *testing.T) {
			rels, err := toolversions.Parse(s.VersionsYAML, s.Name)
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
				if len(rel.Assets) != len(s.Platforms) {
					t.Errorf("release %s has %d assets, want %d", rel.Version, len(rel.Assets), len(s.Platforms))
				}
				for _, plat := range s.Platforms {
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
		})
	}
}

func TestParseSidecarDigest(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "plain hex sha256",
			input: "c04487523e7aa7d1493f7808174c899db93d7a9cf57531420cd76b9bd415f8f4",
			want:  "sha256:c04487523e7aa7d1493f7808174c899db93d7a9cf57531420cd76b9bd415f8f4",
		},
		{
			name:  "with trailing newline and whitespace",
			input: "  c04487523e7aa7d1493f7808174c899db93d7a9cf57531420cd76b9bd415f8f4  \n",
			want:  "sha256:c04487523e7aa7d1493f7808174c899db93d7a9cf57531420cd76b9bd415f8f4",
		},
		{
			name:  "shasum format with filename",
			input: "c04487523e7aa7d1493f7808174c899db93d7a9cf57531420cd76b9bd415f8f4  kubectl\n",
			want:  "sha256:c04487523e7aa7d1493f7808174c899db93d7a9cf57531420cd76b9bd415f8f4",
		},
		{
			name:  "already has sha256 prefix",
			input: "sha256:c04487523e7aa7d1493f7808174c899db93d7a9cf57531420cd76b9bd415f8f4",
			want:  "sha256:c04487523e7aa7d1493f7808174c899db93d7a9cf57531420cd76b9bd415f8f4",
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "whitespace only",
			input:   "   \n\t  ",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := spec.ParseSidecarDigest(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseSidecarDigest(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ParseSidecarDigest(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
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

func TestTarGz_Extract(t *testing.T) {
	tempDir := t.TempDir()
	tgzPath := filepath.Join(tempDir, "archive.tar.gz")

	binContent := []byte("#!/bin/sh\necho mock binary\n")
	otherContent := []byte("readme documentation\n")

	f, err := os.Create(tgzPath)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	// Binary entry
	if err := tw.WriteHeader(&tar.Header{
		Name: "mybin",
		Mode: 0755,
		Size: int64(len(binContent)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(binContent); err != nil {
		t.Fatal(err)
	}

	// Another entry in archive
	if err := tw.WriteHeader(&tar.Header{
		Name: "README.md",
		Mode: 0644,
		Size: int64(len(otherContent)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(otherContent); err != nil {
		t.Fatal(err)
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Extract binary entry
	dest := filepath.Join(tempDir, "extracted", "bin", "mybin")
	if err := (spec.TarGz{}).Extract(tgzPath, "mybin", dest); err != nil {
		t.Fatalf("TarGz.Extract failed: %v", err)
	}

	gotData, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("failed reading extracted binary: %v", err)
	}
	if string(gotData) != string(binContent) {
		t.Errorf("content mismatch: got %q, want %q", string(gotData), string(binContent))
	}
	if !fileutils.IsExecutable(dest) {
		t.Errorf("expected extracted binary %s to be executable", dest)
	}

	// Missing entry errors
	missingDest := filepath.Join(tempDir, "extracted", "bin", "missing")
	if err := (spec.TarGz{}).Extract(tgzPath, "missing", missingDest); err == nil {
		t.Fatal("expected error extracting missing binary from tar.gz, got nil")
	}
}

func TestGitHubReleases_Versions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		releasesJSON := `[
			{"tag_name": "kustomize/v5.8.3", "draft": false, "prerelease": false},
			{"tag_name": "api/v0.21.3", "draft": false, "prerelease": false},
			{"tag_name": "v0.33.0", "draft": false, "prerelease": false},
			{"tag_name": "kustomize/v5.8.2", "draft": false, "prerelease": false},
			{"tag_name": "kustomize/v5.8.1-rc1", "draft": false, "prerelease": true},
			{"tag_name": "v0.32.0", "draft": false, "prerelease": false}
		]`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(releasesJSON))
	}))
	defer server.Close()

	tests := []struct {
		name      string
		tagPrefix string
		wantVers  []string
	}{
		{
			name:      "no prefix",
			tagPrefix: "",
			wantVers:  []string{"v0.33.0", "v0.32.0"},
		},
		{
			name:      "with TagPrefix",
			tagPrefix: "kustomize/",
			wantVers:  []string{"v5.8.3", "v5.8.2"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gh := &spec.GitHubReleases{
				Repo:       "test/repo",
				TagPrefix:  tc.tagPrefix,
				APIBaseURL: server.URL,
			}
			vers, err := gh.Versions(t.Context())
			if err != nil {
				t.Fatalf("Versions failed: %v", err)
			}
			if len(vers) != len(tc.wantVers) {
				t.Fatalf("got %d versions %v, want %d versions %v", len(vers), vers, len(tc.wantVers), tc.wantVers)
			}
			for i := range tc.wantVers {
				if vers[i] != tc.wantVers[i] {
					t.Errorf("version[%d] = %q, want %q", i, vers[i], tc.wantVers[i])
				}
			}
		})
	}
}
