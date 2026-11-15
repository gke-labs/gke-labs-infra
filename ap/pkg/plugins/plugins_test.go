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

package plugins

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
)

func TestCacheKey(t *testing.T) {
	declImage := config.PluginDeclaration{
		Image: "ghcr.io/gke-labs/astro-builder@sha256:4b22",
	}
	if got := CacheKey(declImage); got != "sha256:4b22" {
		t.Errorf("CacheKey(declImage) = %q, want sha256:4b22", got)
	}

	declDocker := config.PluginDeclaration{
		Dockerfile: "images/local/Dockerfile",
	}
	if got := CacheKey(declDocker); got != "images/local/Dockerfile" {
		t.Errorf("CacheKey(declDocker) = %q, want images/local/Dockerfile", got)
	}
}

func TestLoadCachedManifest(t *testing.T) {
	cacheDir := t.TempDir()
	describeDir := filepath.Join(cacheDir, "plugins", "describe")
	if err := os.MkdirAll(describeDir, 0755); err != nil {
		t.Fatal(err)
	}

	manifestJSON := `{
  "apiVersion": "ap.plugin/v1alpha1",
  "name": "astro",
  "activation": ["astro.config.*"],
  "network": false,
  "tasks": [
    {
      "name": "build",
      "verb": "build",
      "description": "Build Astro site",
      "outputs": ["dist"]
    }
  ]
}`
	if err := os.WriteFile(filepath.Join(describeDir, "sha256:4b22.json"), []byte(manifestJSON), 0644); err != nil {
		t.Fatal(err)
	}

	m, err := LoadCachedManifest(cacheDir, "sha256:4b22")
	if err != nil {
		t.Fatalf("LoadCachedManifest failed: %v", err)
	}
	if m.Name != "astro" {
		t.Errorf("manifest name = %q, want astro", m.Name)
	}
	if len(m.Activation) != 1 || m.Activation[0] != "astro.config.*" {
		t.Errorf("manifest activation = %v, want [astro.config.*]", m.Activation)
	}
	if len(m.Tasks) != 1 || m.Tasks[0].Name != "build" {
		t.Errorf("manifest tasks = %v, want 1 task named build", m.Tasks)
	}
}

func TestFindActivatedDirs_HonoursSkips(t *testing.T) {
	sourcetree.ResetCache()
	tempDir := t.TempDir()

	files := map[string]string{
		".git/config":                   "",
		"docs/astro.config.mjs":         "export default {}",
		"docs/src/index.md":             "# Hello",
		"site/astro.config.ts":          "export default {}",
		"ignored/astro.config.mjs":      "export default {}",
		"vendor/astro.config.mjs":       "export default {}",
		"node_modules/astro.config.mjs": "export default {}",
		"other/readme.md":               "# Other",
	}

	for rel, content := range files {
		full := filepath.Join(tempDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	tree, err := sourcetree.Open(tempDir, sourcetree.WithSkip("ignored/**"))
	if err != nil {
		t.Fatalf("sourcetree.Open failed: %v", err)
	}

	activated := FindActivatedDirs(tree, []string{"astro.config.*"})
	docsDir := filepath.Join(tempDir, "docs")
	siteDir := filepath.Join(tempDir, "site")
	ignoredDir := filepath.Join(tempDir, "ignored")
	vendorDir := filepath.Join(tempDir, "vendor")

	hasDocs := false
	hasSite := false
	for _, d := range activated {
		if d == docsDir {
			hasDocs = true
		}
		if d == siteDir {
			hasSite = true
		}
		if d == ignoredDir {
			t.Errorf("FindActivatedDirs returned skipped directory %s", d)
		}
		if d == vendorDir {
			t.Errorf("FindActivatedDirs returned vendor directory %s", d)
		}
	}

	if !hasDocs {
		t.Errorf("expected docs to be activated")
	}
	if !hasSite {
		t.Errorf("expected site to be activated")
	}
	if len(activated) != 2 {
		t.Errorf("expected exactly 2 activated dirs, got %d: %v", len(activated), activated)
	}
}
