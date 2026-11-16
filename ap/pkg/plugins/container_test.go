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
  build)
    if [ -f "$STATE_DIR/fail-build" ]; then
      echo "simulated build failure" >&2
      exit 42
    fi
    echo "$*" > "$STATE_DIR/build-args"
    ;;
  load)
    echo "$*" > "$STATE_DIR/load-args"
    ;;
  image)
    if [ "$2" = "inspect" ]; then
      if [ -f "$STATE_DIR/buildctl-args" ]; then
        echo "sha256:fake-buildctl-image-5678"
      else
        echo "sha256:fake-built-image-1234"
      fi
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
	fakeBuildxPath := filepath.Join(fakeBinDir, "docker-buildx")
	if err := os.WriteFile(fakeBuildxPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	buildctlScript := fmt.Sprintf(`#!/bin/sh
STATE_DIR=%q

if [ -f "$STATE_DIR/fail-buildctl" ]; then
  echo "simulated buildctl failure" >&2
  exit 42
fi

echo "$*" > "$STATE_DIR/buildctl-args"
while [ $# -gt 0 ]; do
  case "$1" in
    --metadata-file)
      cat > "$2" <<'EOF'
{
  "containerimage.digest": "sha256:fake-buildctl-image-5678"
}
EOF
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
`, stateDir)

	fakeBuildctlPath := filepath.Join(fakeBinDir, "buildctl")
	if err := os.WriteFile(fakeBuildctlPath, []byte(buildctlScript), 0755); err != nil {
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
		ImageRef:     declaration.Image,
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
			ImageRef:     declaration.Image,
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
			ImageRef:     declaration.Image,
			Manifest:     manifest,
			Dir:          docsDir,
			RepoRoot:     repoRoot,
		}

		if err := task.Run(t.Context(), scope); err == nil {
			t.Fatal("expected task.Run to fail on escaping output path, got nil")
		}
	})

	t.Run("rejects symlink pointing outside staging", func(t *testing.T) {
		outsideFile := filepath.Join(t.TempDir(), "leak.txt")
		if err := os.WriteFile(outsideFile, []byte("secret"), 0644); err != nil {
			t.Fatal(err)
		}

		mockOut := filepath.Join(stateDir, "mock-out")
		_ = os.RemoveAll(mockOut)
		if err := os.MkdirAll(filepath.Join(mockOut, "dist"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outsideFile, filepath.Join(mockOut, "dist", "leak.txt")); err != nil {
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
			ImageRef:     declaration.Image,
			Manifest:     manifest,
			Dir:          docsDir,
			RepoRoot:     repoRoot,
		}

		if err := task.Run(t.Context(), scope); err == nil {
			t.Fatal("expected task.Run to fail on symlink in staging, got nil")
		}

		leakedPath := filepath.Join(docsDir, "dist", "leak.txt")
		if _, err := os.Stat(leakedPath); !os.IsNotExist(err) {
			t.Errorf("expected leaked file %s not to exist in checkout", leakedPath)
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
		ImageRef:     declaration.Image,
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
		ImageRef:     declaration.Image,
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

func TestDockerfilePlugin(t *testing.T) {
	repoRoot := t.TempDir()
	cacheDirectory := t.TempDir()

	apDirectory := filepath.Join(repoRoot, ".ap")
	if err := os.MkdirAll(apDirectory, 0755); err != nil {
		t.Fatal(err)
	}
	pluginsContent := `
plugins:
  mockplugin:
    dockerfile: images/mock/Dockerfile
`
	if err := os.WriteFile(filepath.Join(apDirectory, "plugins.yaml"), []byte(pluginsContent), 0644); err != nil {
		t.Fatal(err)
	}

	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	fakeBinDir, stateDir := setupFakeDocker(t)
	fakeDockerPath := filepath.Join(fakeBinDir, "docker")
	fakeBuildxPath := filepath.Join(fakeBinDir, "docker-buildx")
	scope := tasks.NewTaskContext(repoRoot, repoRoot, tree).WithTool("docker", fakeDockerPath).WithTool("docker-buildx", fakeBuildxPath)

	t.Run("build invoked with expected arguments and image ID becomes cache key", func(t *testing.T) {
		loadedPlugins, err := plugins.LoadContainerPlugins(t.Context(), scope, cacheDirectory, "")
		if err != nil {
			t.Fatalf("LoadContainerPlugins failed: %v", err)
		}
		if len(loadedPlugins) != 1 {
			t.Fatalf("expected 1 plugin, got %d", len(loadedPlugins))
		}

		containerPlugin, ok := loadedPlugins[0].(*plugins.ContainerPlugin)
		if !ok {
			t.Fatalf("expected *plugins.ContainerPlugin, got %T", loadedPlugins[0])
		}
		if containerPlugin.Declaration.Image != "" {
			t.Errorf("expected Declaration.Image to remain unset, got %q", containerPlugin.Declaration.Image)
		}
		if containerPlugin.ImageRef != "sha256:fake-built-image-1234" {
			t.Errorf("expected ImageRef %q, got %q", "sha256:fake-built-image-1234", containerPlugin.ImageRef)
		}

		buildArgsData, err := os.ReadFile(filepath.Join(stateDir, "build-args"))
		if err != nil {
			t.Fatalf("reading build args: %v", err)
		}
		buildArgs := string(buildArgsData)
		if !strings.HasPrefix(buildArgs, "build ") {
			t.Errorf("expected build command, got: %s", buildArgs)
		}
		if !strings.Contains(buildArgs, "-f images/mock/Dockerfile") {
			t.Errorf("expected -f images/mock/Dockerfile in args, got: %s", buildArgs)
		}
		if !strings.Contains(buildArgs, "-t ap-plugin/mockplugin:latest") {
			t.Errorf("expected -t ap-plugin/mockplugin:latest in args, got: %s", buildArgs)
		}
		if !strings.Contains(buildArgs, "--load") {
			t.Errorf("expected --load in args, got: %s", buildArgs)
		}
		if !strings.HasSuffix(strings.TrimSpace(buildArgs), ".") {
			t.Errorf("expected build context . at end of args, got: %s", buildArgs)
		}

		cachedFile := filepath.Join(cacheDirectory, "plugins", "describe", "sha256-fake-built-image-1234.json")
		if _, err := os.Stat(cachedFile); err != nil {
			t.Errorf("expected cached manifest at %s: %v", cachedFile, err)
		}
	})

	t.Run("with buildkit host set build goes to buildctl and result is loaded", func(t *testing.T) {
		buildkitCacheDirectory := t.TempDir()
		loadedPlugins, err := plugins.LoadContainerPlugins(t.Context(), scope, buildkitCacheDirectory, "tcp://127.0.0.1:2375")
		if err != nil {
			t.Fatalf("LoadContainerPlugins failed: %v", err)
		}
		if len(loadedPlugins) != 1 {
			t.Fatalf("expected 1 plugin, got %d", len(loadedPlugins))
		}

		containerPlugin, ok := loadedPlugins[0].(*plugins.ContainerPlugin)
		if !ok {
			t.Fatalf("expected *plugins.ContainerPlugin, got %T", loadedPlugins[0])
		}
		if containerPlugin.ImageRef != "sha256:fake-buildctl-image-5678" {
			t.Errorf("expected ImageRef %q, got %q", "sha256:fake-buildctl-image-5678", containerPlugin.ImageRef)
		}

		buildctlArgsData, err := os.ReadFile(filepath.Join(stateDir, "buildctl-args"))
		if err != nil {
			t.Fatalf("reading buildctl args: %v", err)
		}
		buildctlArgs := string(buildctlArgsData)
		if !strings.HasPrefix(buildctlArgs, "build ") {
			t.Errorf("expected buildctl build command, got: %s", buildctlArgs)
		}
		if !strings.Contains(buildctlArgs, "--opt filename=images/mock/Dockerfile") {
			t.Errorf("expected filename=images/mock/Dockerfile in args, got: %s", buildctlArgs)
		}
		if !strings.Contains(buildctlArgs, "--output type=docker,name=ap-plugin/mockplugin:latest,dest=") {
			t.Errorf("expected type=docker output in args, got: %s", buildctlArgs)
		}

		loadArgsData, err := os.ReadFile(filepath.Join(stateDir, "load-args"))
		if err != nil {
			t.Fatalf("reading load args: %v", err)
		}
		loadArgs := string(loadArgsData)
		if !strings.Contains(loadArgs, "load -i") {
			t.Errorf("expected load -i in args, got: %s", loadArgs)
		}

		cachedFile := filepath.Join(buildkitCacheDirectory, "plugins", "describe", "sha256-fake-buildctl-image-5678.json")
		if _, err := os.Stat(cachedFile); err != nil {
			t.Errorf("expected cached manifest at %s: %v", cachedFile, err)
		}
	})

	t.Run("failing build is reported", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(stateDir, "fail-build"), []byte("fail"), 0644); err != nil {
			t.Fatal(err)
		}
		emptyCacheDirectory := t.TempDir()
		_, err := plugins.LoadContainerPlugins(t.Context(), scope, emptyCacheDirectory, "")
		if err == nil {
			t.Fatal("expected LoadContainerPlugins to fail when build fails")
		}
		if !strings.Contains(err.Error(), "mockplugin") {
			t.Errorf("expected error to name plugin mockplugin, got: %v", err)
		}
		if !strings.Contains(err.Error(), "images/mock/Dockerfile") {
			t.Errorf("expected error to name Dockerfile images/mock/Dockerfile, got: %v", err)
		}
		if !strings.Contains(err.Error(), "simulated build failure") {
			t.Errorf("expected build output in error, got: %v", err)
		}
	})
}

func TestContainerTask_GetName(t *testing.T) {
	tests := []struct {
		relativeDirectory string
		want              string
	}{
		{relativeDirectory: ".", want: "astro-build"},
		{relativeDirectory: "docs", want: "astro-build docs"},
		{relativeDirectory: "blog", want: "astro-build blog"},
	}
	names := make(map[string]bool)
	for _, tc := range tests {
		task := &plugins.ContainerTask{
			PluginName:        "astro",
			TaskMetadata:      plugins.TaskMetadata{Name: "build"},
			RelativeDirectory: tc.relativeDirectory,
		}
		got := task.GetName()
		if got != tc.want {
			t.Errorf("task.GetName() for relative directory %q = %q, want %q", tc.relativeDirectory, got, tc.want)
		}
		names[got] = true
	}
	if len(names) != len(tests) {
		t.Errorf("expected %d distinct names, got %d", len(tests), len(names))
	}
}
