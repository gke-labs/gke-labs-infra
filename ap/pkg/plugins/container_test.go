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

package plugins_test

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

func setupFakeDocker(t *testing.T) (fakeBinDir string, stateDir string) {
	t.Helper()
	stateDir = t.TempDir()
	fakeBinDir = t.TempDir()

	script := fmt.Sprintf(`#!/bin/sh
STATE_DIR=%q

case "$1" in
  create)
    echo "fake-container-1234"
    ;;
  cp)
    if [ "$2" = "-" ]; then
      cat > "$STATE_DIR/received.tar"
    elif [ -d "$STATE_DIR/mock-out" ]; then
      DEST="$3"
      mkdir -p "$DEST"
      cp -R "$STATE_DIR/mock-out/." "$DEST/"
    fi
    ;;
  start)
    if [ -f "$STATE_DIR/fail-start" ]; then
      echo "simulated start failure" >&2
      exit 42
    fi
    ;;
  rm)
    touch "$STATE_DIR/container-removed"
    ;;
  run)
    if [ "$4" = "describe" ] || [ "$5" = "describe" ] || [ "$6" = "describe" ]; then
      cat <<'EOF'
{
  "apiVersion": "ap.plugin/v1alpha1",
  "name": "mockplugin",
  "activation": ["mock.config"],
  "network": false,
  "tasks": [{"name": "build", "verb": "build", "outputs": ["dist"]}]
}
EOF
    fi
    ;;
esac
`, stateDir)

	fakeDockerPath := filepath.Join(fakeBinDir, "docker")
	if err := os.WriteFile(fakeDockerPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", fakeBinDir+":"+os.Getenv("PATH"))
	return fakeBinDir, stateDir
}

func TestTarStream_WholeRepositoryAndIgnores(t *testing.T) {
	repoRoot := t.TempDir()

	files := map[string]string{
		".git/config":      "[core]",
		"root.txt":         "root content",
		"vendor/dep.txt":   "vendor dependency",
		"docs/mock.config": "",
		"docs/guide.md":    "guide content",
	}

	for relPath, content := range files {
		fullPath := filepath.Join(repoRoot, relPath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	fakeBinDir, stateDir := setupFakeDocker(t)
	fakeDockerPath := filepath.Join(fakeBinDir, "docker")

	docsDir := filepath.Join(repoRoot, "docs")
	scope := tasks.NewTaskContext(repoRoot, docsDir, tree.Under(docsDir)).WithTool("docker", fakeDockerPath)

	declaration := config.PluginDeclaration{Image: "example.com/mock@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"}
	manifest := &plugins.PluginManifest{
		Name:  "mockplugin",
		Tasks: []plugins.TaskMetadata{{Name: "build", Verb: "build"}},
	}

	task := &plugins.ContainerTask{
		PluginName:   "mockplugin",
		TaskMetadata: manifest.Tasks[0],
		Declaration:  declaration,
		Manifest:     manifest,
		Dir:          docsDir,
		RepoRoot:     repoRoot,
	}

	if err := task.Run(t.Context(), scope); err != nil {
		t.Fatalf("task.Run failed: %v", err)
	}

	tarData, err := os.ReadFile(filepath.Join(stateDir, "received.tar"))
	if err != nil {
		t.Fatalf("reading received tar: %v", err)
	}

	tarReader := tar.NewReader(bytes.NewReader(tarData))
	var entryNames []string
	for {
		header, readErr := tarReader.Next()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("reading tar header: %v", readErr)
		}
		entryNames = append(entryNames, header.Name)
	}

	if !slices.Contains(entryNames, "out/") {
		t.Errorf("expected tar to contain out/, got: %v", entryNames)
	}
	if !slices.Contains(entryNames, "src/root.txt") {
		t.Errorf("expected tar to contain whole repo file src/root.txt, got: %v", entryNames)
	}
	if !slices.Contains(entryNames, "src/docs/guide.md") {
		t.Errorf("expected tar to contain src/docs/guide.md, got: %v", entryNames)
	}
	if slices.Contains(entryNames, "src/vendor/dep.txt") {
		t.Errorf("expected vendor/dep.txt to be omitted from tar stream")
	}
	for _, entry := range entryNames {
		if strings.HasPrefix(entry, "src/.git/") || entry == "src/.git" {
			t.Errorf("expected .git to be omitted from tar stream, got entry: %s", entry)
		}
	}
}

func TestOutputPlacement_UntrustedRoot(t *testing.T) {
	repoRoot := t.TempDir()
	docsDir := filepath.Join(repoRoot, "docs")
	if err := os.MkdirAll(docsDir, 0755); err != nil {
		t.Fatal(err)
	}

	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	fakeBinDir, stateDir := setupFakeDocker(t)
	fakeDockerPath := filepath.Join(fakeBinDir, "docker")
	scope := tasks.NewTaskContext(repoRoot, docsDir, tree.Under(docsDir)).WithTool("docker", fakeDockerPath)

	declaration := config.PluginDeclaration{Image: "example.com/mock@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"}

	t.Run("places outputs under activated directory", func(t *testing.T) {
		mockOut := filepath.Join(stateDir, "mock-out")
		if err := os.MkdirAll(filepath.Join(mockOut, "dist"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(mockOut, "dist", "site.html"), []byte("site output"), 0644); err != nil {
			t.Fatal(err)
		}

		manifest := &plugins.PluginManifest{
			Name:  "mockplugin",
			Tasks: []plugins.TaskMetadata{{Name: "build", Verb: "build", Outputs: []string{"dist"}}},
		}
		task := &plugins.ContainerTask{
			PluginName:   "mockplugin",
			TaskMetadata: manifest.Tasks[0],
			Declaration:  declaration,
			Manifest:     manifest,
			Dir:          docsDir,
			RepoRoot:     repoRoot,
		}

		if err := task.Run(t.Context(), scope); err != nil {
			t.Fatalf("task.Run failed: %v", err)
		}

		data, err := os.ReadFile(filepath.Join(docsDir, "dist", "site.html"))
		if err != nil {
			t.Fatalf("expected output file to exist: %v", err)
		}
		if string(data) != "site output" {
			t.Errorf("got %q, want %q", string(data), "site output")
		}
	})

	t.Run("rejects path escape or git tampering", func(t *testing.T) {
		mockOut := filepath.Join(stateDir, "mock-out")
		if err := os.MkdirAll(mockOut, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(mockOut, "evil.txt"), []byte("evil"), 0644); err != nil {
			t.Fatal(err)
		}

		manifest := &plugins.PluginManifest{
			Name:  "mockplugin",
			Tasks: []plugins.TaskMetadata{{Name: "build", Verb: "build", Outputs: []string{"../evil.txt"}}},
		}
		task := &plugins.ContainerTask{
			PluginName:   "mockplugin",
			TaskMetadata: manifest.Tasks[0],
			Declaration:  declaration,
			Manifest:     manifest,
			Dir:          docsDir,
			RepoRoot:     repoRoot,
		}

		if err := task.Run(t.Context(), scope); err == nil {
			t.Fatal("expected task.Run to fail on escaping output path, got nil")
		}
	})
}

func TestNetworkAuthorization(t *testing.T) {
	repoRoot := t.TempDir()
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	scope := tasks.NewTaskContext(repoRoot, repoRoot, tree)
	declaration := config.PluginDeclaration{
		Image:   "example.com/mock@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		Network: false,
	}
	manifest := &plugins.PluginManifest{
		Name:    "mockplugin",
		Network: true,
		Tasks:   []plugins.TaskMetadata{{Name: "fetch", Verb: "test"}},
	}

	task := &plugins.ContainerTask{
		PluginName:   "mockplugin",
		TaskMetadata: manifest.Tasks[0],
		Declaration:  declaration,
		Manifest:     manifest,
		Dir:          repoRoot,
		RepoRoot:     repoRoot,
	}

	err = task.Run(t.Context(), scope)
	if err == nil {
		t.Fatal("expected error when network requested without consent in .ap/plugins.yaml")
	}
	if !strings.Contains(err.Error(), "requires network access, but network is not authorized") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestContainerRemovalOnFailure(t *testing.T) {
	repoRoot := t.TempDir()
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	fakeBinDir, stateDir := setupFakeDocker(t)
	fakeDockerPath := filepath.Join(fakeBinDir, "docker")
	if err := os.WriteFile(filepath.Join(stateDir, "fail-start"), []byte("fail"), 0644); err != nil {
		t.Fatal(err)
	}

	scope := tasks.NewTaskContext(repoRoot, repoRoot, tree).WithTool("docker", fakeDockerPath)
	declaration := config.PluginDeclaration{Image: "example.com/mock@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"}
	manifest := &plugins.PluginManifest{
		Name:  "mockplugin",
		Tasks: []plugins.TaskMetadata{{Name: "build", Verb: "build"}},
	}

	task := &plugins.ContainerTask{
		PluginName:   "mockplugin",
		TaskMetadata: manifest.Tasks[0],
		Declaration:  declaration,
		Manifest:     manifest,
		Dir:          repoRoot,
		RepoRoot:     repoRoot,
	}

	err = task.Run(t.Context(), scope)
	if err == nil {
		t.Fatal("expected task.Run to fail, got nil")
	}

	if _, statErr := os.Stat(filepath.Join(stateDir, "container-removed")); statErr != nil {
		t.Errorf("expected container to be removed on failure, stat error: %v", statErr)
	}
}
