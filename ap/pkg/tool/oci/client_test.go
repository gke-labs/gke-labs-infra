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

package oci

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseImageRef(t *testing.T) {
	tests := []struct {
		raw          string
		wantRegistry string
		wantRepo     string
		wantTag      string
		wantDigest   string
		wantErr      bool
	}{
		{
			raw:          "ghcr.io/gke-labs/tools/protoc:21.12",
			wantRegistry: "ghcr.io",
			wantRepo:     "gke-labs/tools/protoc",
			wantTag:      "21.12",
		},
		{
			raw:          "gke-labs/tools/protoc:v1.36.12",
			wantRegistry: "ghcr.io",
			wantRepo:     "gke-labs/tools/protoc",
			wantTag:      "v1.36.12",
		},
		{
			raw:          "us-docker.pkg.dev/gke-labs/tools/protoc@sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
			wantRegistry: "us-docker.pkg.dev",
			wantRepo:     "gke-labs/tools/protoc",
			wantDigest:   "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
		},
		{
			raw:          "localhost:5000/tools/protoc:latest",
			wantRegistry: "localhost:5000",
			wantRepo:     "tools/protoc",
			wantTag:      "latest",
		},
		{
			raw:     "",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			ref, err := ParseImageRef(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got nil", tc.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ref.Registry != tc.wantRegistry {
				t.Errorf("Registry: got %q, want %q", ref.Registry, tc.wantRegistry)
			}
			if ref.Repository != tc.wantRepo {
				t.Errorf("Repository: got %q, want %q", ref.Repository, tc.wantRepo)
			}
			if ref.Tag != tc.wantTag {
				t.Errorf("Tag: got %q, want %q", ref.Tag, tc.wantTag)
			}
			if ref.Digest != tc.wantDigest {
				t.Errorf("Digest: got %q, want %q", ref.Digest, tc.wantDigest)
			}
		})
	}
}

func createTestLayer(t *testing.T, files map[string]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for name, content := range files {
		data := []byte(content)
		hdr := &tar.Header{
			Name: name,
			Mode: 0755,
			Size: int64(len(data)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("write tar body: %v", err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}

	compressed := buf.Bytes()
	hash := sha256.Sum256(compressed)
	digest := "sha256:" + hex.EncodeToString(hash[:])
	return compressed, digest
}

func TestPullLayer_MultiArch_AndCache(t *testing.T) {
	ctx := t.Context()

	// Prepare layer content
	layerAmd64, digestAmd64 := createTestLayer(t, map[string]string{
		"bin/protoc":                        "fake-amd64-protoc-binary",
		"include/google/protobuf/any.proto": "syntax = \"proto3\";",
	})
	layerArm64, digestArm64 := createTestLayer(t, map[string]string{
		"bin/protoc":                        "fake-arm64-protoc-binary",
		"include/google/protobuf/any.proto": "syntax = \"proto3\";",
	})

	// Prepare child manifests
	manifestAmd64 := Manifest{
		SchemaVersion: 2,
		MediaType:     MediaTypeOCIManifest,
		Layers: []Descriptor{
			{
				MediaType: MediaTypeOCILayer,
				Digest:    digestAmd64,
				Size:      int64(len(layerAmd64)),
			},
		},
	}
	bytesManifestAmd64, _ := json.Marshal(manifestAmd64)
	hashManifestAmd64 := "sha256:" + hex.EncodeToString(sha256Sum(bytesManifestAmd64))

	manifestArm64 := Manifest{
		SchemaVersion: 2,
		MediaType:     MediaTypeOCIManifest,
		Layers: []Descriptor{
			{
				MediaType: MediaTypeOCILayer,
				Digest:    digestArm64,
				Size:      int64(len(layerArm64)),
			},
		},
	}
	bytesManifestArm64, _ := json.Marshal(manifestArm64)
	hashManifestArm64 := "sha256:" + hex.EncodeToString(sha256Sum(bytesManifestArm64))

	// Multi-arch index
	index := ManifestList{
		SchemaVersion: 2,
		MediaType:     MediaTypeOCIIndex,
		Manifests: []Descriptor{
			{
				MediaType: MediaTypeOCIManifest,
				Digest:    hashManifestAmd64,
				Size:      int64(len(bytesManifestAmd64)),
				Platform: &Platform{
					OS:           "linux",
					Architecture: "amd64",
				},
			},
			{
				MediaType: MediaTypeOCIManifest,
				Digest:    hashManifestArm64,
				Size:      int64(len(bytesManifestArm64)),
				Platform: &Platform{
					OS:           "linux",
					Architecture: "arm64",
				},
			},
		},
	}
	bytesIndex, _ := json.Marshal(index)
	hashIndex := "sha256:" + hex.EncodeToString(sha256Sum(bytesIndex))

	const validToken = "test-bearer-token-12345"

	// Mock OCI Registry server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check auth
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+validToken && !strings.HasPrefix(r.URL.Path, "/token") {
			w.Header().Set("Www-Authenticate", fmt.Sprintf(`Bearer realm="http://%s/token",service="test-registry",scope="repository:tools/protoc:pull"`, r.Host))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		switch {
		case r.URL.Path == "/token":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"token": validToken,
			})
		case r.URL.Path == "/v2/tools/protoc/manifests/21.12" || r.URL.Path == "/v2/tools/protoc/manifests/"+hashIndex:
			w.Header().Set("Content-Type", MediaTypeOCIIndex)
			w.Header().Set("Docker-Content-Digest", hashIndex)
			_, _ = w.Write(bytesIndex)
		case r.URL.Path == "/v2/tools/protoc/manifests/"+hashManifestAmd64:
			w.Header().Set("Content-Type", MediaTypeOCIManifest)
			w.Header().Set("Docker-Content-Digest", hashManifestAmd64)
			_, _ = w.Write(bytesManifestAmd64)
		case r.URL.Path == "/v2/tools/protoc/manifests/"+hashManifestArm64:
			w.Header().Set("Content-Type", MediaTypeOCIManifest)
			w.Header().Set("Docker-Content-Digest", hashManifestArm64)
			_, _ = w.Write(bytesManifestArm64)
		case r.URL.Path == "/v2/tools/protoc/blobs/"+digestAmd64:
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(layerAmd64)
		case r.URL.Path == "/v2/tools/protoc/blobs/"+digestArm64:
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(layerArm64)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	client := NewClient(server.Client())
	destDir := t.TempDir()

	// Pull arm64 layer
	opts := PullOptions{
		ImageRef:     fmt.Sprintf("%s/tools/protoc:21.12", u.Host),
		Platform:     Platform{OS: "linux", Architecture: "arm64"},
		DestDir:      destDir,
		InsecureHTTP: true,
	}

	res, err := client.PullLayer(ctx, opts)
	if err != nil {
		t.Fatalf("PullLayer failed: %v", err)
	}
	if res.Cached {
		t.Errorf("first pull should not be cached")
	}
	if res.LayerDigest != digestArm64 {
		t.Errorf("LayerDigest: got %s, want %s", res.LayerDigest, digestArm64)
	}

	// Verify extracted files
	binPath := filepath.Join(destDir, "bin", "protoc")
	binContent, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("reading extracted bin: %v", err)
	}
	if string(binContent) != "fake-arm64-protoc-binary" {
		t.Errorf("bin content mismatch: got %q", string(binContent))
	}

	fi, err := os.Stat(binPath)
	if err != nil {
		t.Fatalf("stat bin: %v", err)
	}
	if fi.Mode()&0111 == 0 {
		t.Errorf("expected executable bit on bin, got mode %v", fi.Mode())
	}

	includePath := filepath.Join(destDir, "include", "google", "protobuf", "any.proto")
	if _, err := os.Stat(includePath); err != nil {
		t.Errorf("expected include file at %s: %v", includePath, err)
	}

	// Second pull: verify cache hit
	res2, err := client.PullLayer(ctx, opts)
	if err != nil {
		t.Fatalf("second PullLayer failed: %v", err)
	}
	if !res2.Cached {
		t.Errorf("second pull should have been cached")
	}
}

func TestPullLayer_ChecksumMismatch(t *testing.T) {
	ctx := t.Context()

	// Layer with valid gzip but different content from manifest
	goodLayer, goodDigest := createTestLayer(t, map[string]string{
		"bin/protoc": "real-protoc-content",
	})
	tamperedLayer, _ := createTestLayer(t, map[string]string{
		"bin/protoc": "tampered-protoc-content",
	})

	manifest := Manifest{
		SchemaVersion: 2,
		MediaType:     MediaTypeOCIManifest,
		Layers: []Descriptor{
			{
				MediaType: MediaTypeOCILayer,
				Digest:    goodDigest,
				Size:      int64(len(goodLayer)),
			},
		},
	}
	manifestBytes, _ := json.Marshal(manifest)
	manifestDigest := "sha256:" + hex.EncodeToString(sha256Sum(manifestBytes))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/tools/protoc/manifests/v1":
			w.Header().Set("Content-Type", MediaTypeOCIManifest)
			w.Header().Set("Docker-Content-Digest", manifestDigest)
			_, _ = w.Write(manifestBytes)
		case r.URL.Path == "/v2/tools/protoc/blobs/"+goodDigest:
			// Return tampered layer
			_, _ = w.Write(tamperedLayer)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	u, _ := url.Parse(server.URL)
	client := NewClient(server.Client())
	destDir := t.TempDir()

	_, err := client.PullLayer(ctx, PullOptions{
		ImageRef:     fmt.Sprintf("%s/tools/protoc:v1", u.Host),
		DestDir:      destDir,
		InsecureHTTP: true,
	})
	if err == nil {
		t.Fatalf("expected error on layer checksum mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("unexpected error message: %v", err)
	}

	entries, _ := os.ReadDir(destDir)
	if len(entries) > 0 {
		t.Errorf("expected destDir to be empty after checksum failure, found %d entries", len(entries))
	}
}

func TestPullLayer_PathTraversal(t *testing.T) {
	ctx := t.Context()

	// Malicious layer with ../ outside root
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	_ = tw.WriteHeader(&tar.Header{
		Name: "../escape.txt",
		Mode: 0644,
		Size: 4,
	})
	_, _ = tw.Write([]byte("evil"))
	_ = tw.Close()
	_ = gw.Close()

	badLayer := buf.Bytes()
	hash := sha256.Sum256(badLayer)
	digest := "sha256:" + hex.EncodeToString(hash[:])

	manifest := Manifest{
		SchemaVersion: 2,
		MediaType:     MediaTypeOCIManifest,
		Layers: []Descriptor{
			{
				MediaType: MediaTypeOCILayer,
				Digest:    digest,
				Size:      int64(len(badLayer)),
			},
		},
	}
	mBytes, _ := json.Marshal(manifest)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/manifests/"):
			w.Header().Set("Content-Type", MediaTypeOCIManifest)
			_, _ = w.Write(mBytes)
		case strings.Contains(r.URL.Path, "/blobs/"):
			_, _ = w.Write(badLayer)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	u, _ := url.Parse(server.URL)
	client := NewClient(server.Client())
	destDir := t.TempDir()

	_, err := client.PullLayer(ctx, PullOptions{
		ImageRef:     fmt.Sprintf("%s/tools/evil:v1", u.Host),
		DestDir:      destDir,
		InsecureHTTP: true,
	})
	if err == nil {
		t.Fatalf("expected error on path traversal, got nil")
	}
	if !strings.Contains(err.Error(), "illegal file path") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func sha256Sum(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}
