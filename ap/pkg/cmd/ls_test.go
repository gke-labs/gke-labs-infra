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

	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins"
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
  "activation": ["docs/astro.config.*"],
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
	if err := os.WriteFile(filepath.Join(describeDir, "sha256-4b22.json"), []byte(manifestJSON), 0644); err != nil {
		t.Fatal(err)
	}

	rootOpt := &RootOptions{RepoRoot: tempDir, Dir: tempDir}
	output := captureStdout(t, func() {
		if err := RunLs(t.Context(), LsOptions{RootOptions: rootOpt}); err != nil {
			t.Fatalf("RunLs failed: %v", err)
		}
	})

	if !strings.Contains(output, "docs\n  - build-tasks\n    - astro-build") {
		t.Errorf("expected output to contain docs build-tasks astro-build, got:\n%s", output)
	}
}

func TestRunLs_DockerfilePlugin_SameManifest(t *testing.T) {
	sourcetree.ResetCache()
	repoRoot := t.TempDir()
	cacheDirectory := t.TempDir()
	t.Setenv("AP_CACHE_DIR", cacheDirectory)

	mockToolScript := `#!/bin/sh
case "$1" in
  build)
    while [ $# -gt 0 ]; do
      case "$1" in
        --iidfile)
          echo "sha256:fake-dockerfile-image-1234" > "$2"
          shift 2
          ;;
        *)
          shift
          ;;
      esac
    done
    ;;
  image)
    if [ "$2" = "inspect" ]; then
      echo "sha256:fake-dockerfile-image-1234"
    fi
    ;;
  run)
    cat <<'EOF'
{
  "apiVersion": "ap.plugin/v1alpha1",
  "name": "demo",
  "activation": ["docs/demo.config"],
  "network": false,
  "tasks": [
    {
      "name": "build",
      "verb": "build",
      "description": "Build demo",
      "outputs": ["dist"]
    }
  ]
}
EOF
    ;;
esac
`

	dockerBinDirectory := filepath.Join(cacheDirectory, "tools", "docker", "29.9.2", "bin")
	if err := os.MkdirAll(dockerBinDirectory, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dockerBinDirectory, "docker"), []byte(mockToolScript), 0755); err != nil {
		t.Fatal(err)
	}

	buildxBinDirectory := filepath.Join(cacheDirectory, "tools", "docker-buildx", "v0.38.1", "bin")
	if err := os.MkdirAll(buildxBinDirectory, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(buildxBinDirectory, "docker-buildx"), []byte(mockToolScript), 0755); err != nil {
		t.Fatal(err)
	}

	files := map[string]string{
		".git/config": "",
		".ap/ap.yaml": "version: '!self'\n",
		".ap/plugins.yaml": `
plugins:
  demo:
    dockerfile: tools/demo/Dockerfile
`,
		"tools/demo/Dockerfile": "FROM alpine\n",
		"docs/demo.config":      "key=value\n",
	}

	for relativePath, fileContent := range files {
		fullPath := filepath.Join(repoRoot, relativePath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(fileContent), 0644); err != nil {
			t.Fatal(err)
		}
	}

	rootOptions := &RootOptions{RepoRoot: repoRoot, Dir: repoRoot}
	listingOutput := captureStdout(t, func() {
		if err := RunLs(t.Context(), LsOptions{RootOptions: rootOptions}); err != nil {
			t.Fatalf("RunLs failed: %v", err)
		}
	})

	if !strings.Contains(listingOutput, "docs\n  - build-tasks\n    - demo-build") {
		t.Errorf("expected listing output to contain docs build-tasks demo-build, got:\n%s", listingOutput)
	}

	sourceTree, err := rootOptions.GetSourceTree()
	if err != nil {
		t.Fatalf("GetSourceTree failed: %v", err)
	}
	scopes, err := DiscoverScopes(t.Context(), sourceTree, repoRoot, repoRoot)
	if err != nil {
		t.Fatalf("DiscoverScopes failed: %v", err)
	}
	if len(scopes) == 0 {
		t.Fatalf("expected at least 1 scope, got 0")
	}

	var discoveredContainerTask *plugins.ContainerTask
	for _, task := range scopes[0].BuildTasks {
		if containerTask, ok := task.(*plugins.ContainerTask); ok && containerTask.PluginName == "demo" {
			discoveredContainerTask = containerTask
			break
		}
	}
	if discoveredContainerTask == nil {
		t.Fatalf("expected demo plugin task in scope.BuildTasks, got: %v", scopes[0].BuildTasks)
	}
	if discoveredContainerTask.Manifest.Name != "demo" {
		t.Errorf("expected manifest name 'demo', got: %s", discoveredContainerTask.Manifest.Name)
	}
	if discoveredContainerTask.ImageRef != "sha256:fake-dockerfile-image-1234" {
		t.Errorf("expected ImageRef 'sha256:fake-dockerfile-image-1234', got: %s", discoveredContainerTask.ImageRef)
	}

	cachedManifestPath := filepath.Join(cacheDirectory, "plugins", "describe", "sha256-fake-dockerfile-image-1234.json")
	if _, err := os.Stat(cachedManifestPath); err != nil {
		t.Errorf("expected cached manifest at %s: %v", cachedManifestPath, err)
	}
}
