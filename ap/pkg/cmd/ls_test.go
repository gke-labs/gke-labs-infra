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

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
)

func TestRunLs_ActivatedPlugin(t *testing.T) {
	sourcetree.ResetCache()
	tempDir := t.TempDir()
	cacheDir := t.TempDir()
	t.Setenv("AP_CACHE_DIR", cacheDir)

	files := map[string]string{
		".git/config": "",
		".ap/ap.yaml": "version: '!self'\n",
		".ap/plugins.yaml": `
plugins:
  astro:
    image: ghcr.io/gke-labs/astro-builder@sha256:4b22
    network: false
`,
		"docs/astro.config.mjs": "export default {}",
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

	manifestJSON := `{
  "apiVersion": "ap.plugin/v1alpha1",
  "name": "astro",
  "activation": ["astro.config.*"],
  "network": false,
  "tasks": [
    {
      "name": "build",
      "verb": "build",
      "description": "Build Astro static documentation site",
      "outputs": ["dist"]
    }
  ]
}`
	describeDir := filepath.Join(cacheDir, "plugins", "describe")
	if err := os.MkdirAll(describeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(describeDir, "sha256:4b22.json"), []byte(manifestJSON), 0644); err != nil {
		t.Fatal(err)
	}

	// Test 1: Run ls from repo root
	rootOpt := &RootOptions{RepoRoot: tempDir, Dir: tempDir}
	output := captureStdout(t, func() {
		if err := RunLs(t.Context(), LsOptions{RootOptions: rootOpt}); err != nil {
			t.Fatalf("RunLs failed: %v", err)
		}
	})

	if !strings.Contains(output, "docs\n  - build-tasks\n    - astro-build") {
		t.Errorf("expected output to contain docs build-tasks astro-build, got:\n%s", output)
	}

	// Test 2: Run ls docs
	sourcetree.ResetCache()
	docsOpt := &RootOptions{RepoRoot: tempDir, Dir: filepath.Join(tempDir, "docs")}
	outputDocs := captureStdout(t, func() {
		if err := RunLs(t.Context(), LsOptions{RootOptions: docsOpt}); err != nil {
			t.Fatalf("RunLs failed: %v", err)
		}
	})

	if !strings.Contains(outputDocs, "- build-tasks\n    - astro-build") {
		t.Errorf("expected outputDocs to contain build-tasks astro-build, got:\n%s", outputDocs)
	}
}

func TestRunLs_ConflictingOutputs(t *testing.T) {
	sourcetree.ResetCache()
	tempDir := t.TempDir()
	cacheDir := t.TempDir()
	t.Setenv("AP_CACHE_DIR", cacheDir)

	files := map[string]string{
		".git/config": "",
		".ap/ap.yaml": "version: '!self'\n",
		".ap/plugins.yaml": `
plugins:
  p1:
    image: repo/p1@sha256:1111
  p2:
    image: repo/p2@sha256:2222
`,
		"site/match.txt": "content",
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

	describeDir := filepath.Join(cacheDir, "plugins", "describe")
	if err := os.MkdirAll(describeDir, 0755); err != nil {
		t.Fatal(err)
	}

	m1 := `{"apiVersion":"ap.plugin/v1alpha1","name":"p1","activation":["match.txt"],"tasks":[{"name":"t1","verb":"build","outputs":["dist"]}]}`
	m2 := `{"apiVersion":"ap.plugin/v1alpha1","name":"p2","activation":["match.txt"],"tasks":[{"name":"t2","verb":"build","outputs":["dist"]}]}`
	if err := os.WriteFile(filepath.Join(describeDir, "sha256:1111.json"), []byte(m1), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(describeDir, "sha256:2222.json"), []byte(m2), 0644); err != nil {
		t.Fatal(err)
	}

	rootOpt := &RootOptions{RepoRoot: tempDir, Dir: tempDir}
	err := RunLs(t.Context(), LsOptions{RootOptions: rootOpt})
	if err == nil {
		t.Fatalf("expected error on conflicting output paths, got nil")
	}
	if !strings.Contains(err.Error(), "conflicting output path") {
		t.Errorf("expected error mentioning conflicting output path, got: %v", err)
	}
}
