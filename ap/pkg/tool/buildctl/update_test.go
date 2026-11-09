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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/github"
	toolversions "github.com/gke-labs/gke-labs-infra/ap/pkg/tool/versions"
)

func TestUpdate(t *testing.T) {
	dummyHash := "c04487523e7aa7d1493f7808174c899db93d7a9cf57531420cd76b9bd415f8f4"

	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/moby/buildkit/releases" {
			releases := []github.Release{
				{
					TagName: "v0.34.2",
					Assets: []github.Asset{
						{Name: "buildkit-v0.34.2.linux-amd64.tar.gz", BrowserDownloadURL: serverURL + "/download/buildkit-v0.34.2.linux-amd64.tar.gz"},
						{Name: "buildkit-v0.34.2.linux-amd64.provenance.json", BrowserDownloadURL: serverURL + "/download/buildkit-v0.34.2.linux-amd64.provenance.json"},
						{Name: "buildkit-v0.34.2.linux-arm64.tar.gz", BrowserDownloadURL: serverURL + "/download/buildkit-v0.34.2.linux-arm64.tar.gz"},
						{Name: "buildkit-v0.34.2.linux-arm64.provenance.json", BrowserDownloadURL: serverURL + "/download/buildkit-v0.34.2.linux-arm64.provenance.json"},
						{Name: "buildkit-v0.34.2.darwin-amd64.tar.gz", BrowserDownloadURL: serverURL + "/download/buildkit-v0.34.2.darwin-amd64.tar.gz"},
						{Name: "buildkit-v0.34.2.darwin-amd64.provenance.json", BrowserDownloadURL: serverURL + "/download/buildkit-v0.34.2.darwin-amd64.provenance.json"},
						{Name: "buildkit-v0.34.2.darwin-arm64.tar.gz", BrowserDownloadURL: serverURL + "/download/buildkit-v0.34.2.darwin-arm64.tar.gz"},
						{Name: "buildkit-v0.34.2.darwin-arm64.provenance.json", BrowserDownloadURL: serverURL + "/download/buildkit-v0.34.2.darwin-arm64.provenance.json"},
						{Name: "buildkit-v0.34.2.windows-amd64.tar.gz", BrowserDownloadURL: serverURL + "/download/buildkit-v0.34.2.windows-amd64.tar.gz"},
						{Name: "buildkit-v0.34.2.windows-amd64.provenance.json", BrowserDownloadURL: serverURL + "/download/buildkit-v0.34.2.windows-amd64.provenance.json"},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(releases)
			return
		}

		if filepath.Ext(r.URL.Path) == ".json" {
			base := filepath.Base(r.URL.Path)
			// e.g. buildkit-v0.34.2.linux-amd64.provenance.json -> buildkit-v0.34.2.linux-amd64.tar.gz
			tarGzName := base[:len(base)-len(".provenance.json")] + ".tar.gz"
			prov := provenanceStatement{
				Subject: []struct {
					Name   string            `json:"name"`
					Digest map[string]string `json:"digest"`
				}{
					{
						Name: tarGzName,
						Digest: map[string]string{
							"sha256": dummyHash,
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(prov)
			return
		}

		http.NotFound(w, r)
	}))
	defer server.Close()
	serverURL = server.URL

	tempRepo := t.TempDir()
	targetDir := filepath.Join(tempRepo, "ap", "pkg", "tool", "buildctl")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("BUILDCTL_GITHUB_API_BASE_URL", server.URL)

	if err := Update(t.Context(), tempRepo); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	yamlPath := filepath.Join(targetDir, "versions.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("reading generated versions.yaml: %v", err)
	}

	releases, err := toolversions.Parse(data, "buildctl")
	if err != nil {
		t.Fatalf("parsing generated versions.yaml: %v", err)
	}

	if len(releases) != 1 {
		t.Fatalf("expected 1 release, got %d", len(releases))
	}

	if releases[0].Version != "v0.34.2" {
		t.Errorf("expected newest release v0.34.2, got %s", releases[0].Version)
	}

	for _, plat := range supportedPlatforms {
		digest, ok := releases[0].Assets[plat]
		if !ok {
			t.Errorf("missing asset for %s in %s", plat, releases[0].Version)
		}
		if digest != "sha256:"+dummyHash {
			t.Errorf("expected digest sha256:%s, got %s", dummyHash, digest)
		}
	}
}
