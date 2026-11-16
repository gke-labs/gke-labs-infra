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
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

var (
	mockPluginImageOnce sync.Once
	mockPluginImageRef  string
	mockPluginImageErr  error
)

func buildMockPluginImage(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found, skipping container plugin test")
	}

	mockPluginImageOnce.Do(func() {
		mockPluginImageRef, mockPluginImageErr = buildMockPluginImageOnce()
	})
	if mockPluginImageErr != nil {
		t.Fatalf("failed to build test plugin image: %v", mockPluginImageErr)
	}
	return mockPluginImageRef
}

func buildMockPluginImageOnce() (string, error) {
	tempDir, err := os.MkdirTemp("", "ap-mock-plugin-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tempDir)
	entrypointScript := `#!/bin/sh
case "$1" in
  describe)
    cat <<'EOF'
{
  "apiVersion": "ap.plugin/v1alpha1",
  "name": "mockplugin",
  "activation": ["mock.config"],
  "network": false,
  "tasks": [
    {
      "name": "build",
      "verb": "build",
      "description": "Mock build task",
      "outputs": ["dist"]
    },
    {
      "name": "gen",
      "verb": "generate",
      "description": "Mock generate task",
      "outputs": ["."]
    },
    {
      "name": "fail",
      "verb": "build",
      "description": "Mock fail task",
      "outputs": []
    },
    {
      "name": "fetch",
      "verb": "test",
      "description": "Mock fetch task",
      "outputs": []
    },
    {
      "name": "sleep",
      "verb": "test",
      "description": "Mock long task",
      "outputs": []
    }
  ]
}
EOF
    ;;
  run)
    TASK="$2"
    shift 2
    SRC=""
    OUT=""
    while [ $# -gt 0 ]; do
      case "$1" in
        --src) SRC="$2"; shift 2 ;;
        --out) OUT="$2"; shift 2 ;;
        *) shift ;;
      esac
    done
    case "$TASK" in
      build)
        mkdir -p "$OUT/dist"
        echo "built: $(cat "$SRC/input.txt")" > "$OUT/dist/output.html"
        ;;
      gen)
        echo "generated: $(cat "$SRC/input.txt")" > "$OUT/generated.txt"
        ;;
      fail)
        echo "intentional plugin failure message" >&2
        exit 42
        ;;
      fetch)
        wget -T 1 -q -O - http://google.com
        ;;
      sleep)
        sleep 10
        ;;
      *)
        echo "unknown task $TASK" >&2
        exit 1
        ;;
    esac
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(tempDir, "entrypoint.sh"), []byte(entrypointScript), 0755); err != nil {
		return "", err
	}

	dockerfile := `FROM alpine:3.22
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
`
	if err := os.WriteFile(filepath.Join(tempDir, "Dockerfile"), []byte(dockerfile), 0644); err != nil {
		return "", err
	}

	tag := fmt.Sprintf("ap-mock-plugin:%d", time.Now().UnixNano())
	cmd := exec.Command("docker", "build", "-t", tag, tempDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker build failed: %w (%s)", err, out)
	}

	inspectCmd := exec.Command("docker", "inspect", "-f", "{{.Id}}", tag)
	idOut, err := inspectCmd.Output()
	if err != nil {
		return "", fmt.Errorf("docker inspect failed: %w", err)
	}
	imageID := strings.TrimSpace(string(idOut))

	return fmt.Sprintf("%s@%s", tag, imageID), nil
}

func setupTestWorkspace(t *testing.T, imageRef string) (string, string) {
	t.Helper()
	repoRoot := t.TempDir()

	files := map[string]string{
		".git/config":         "",
		"docs/mock.config":    "",
		"docs/input.txt":      "hello world",
		".ap/plugins.yaml": fmt.Sprintf(`plugins:
  mockplugin:
    image: %s
    network: false
`, imageRef),
	}

	for rel, content := range files {
		fullPath := filepath.Join(repoRoot, rel)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	docsDir := filepath.Join(repoRoot, "docs")
	return repoRoot, docsDir
}

func TestContainerTask_BuildAndOutputPlacement(t *testing.T) {
	imageRef := buildMockPluginImage(t)
	repoRoot, docsDir := setupTestWorkspace(t, imageRef)

	cacheDir := t.TempDir()
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	pluginsList, err := plugins.LoadContainerPlugins(t.Context(), repoRoot, cacheDir)
	if err != nil {
		t.Fatalf("LoadContainerPlugins failed: %v", err)
	}
	if len(pluginsList) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(pluginsList))
	}

	scope := tasks.NewTaskContext(repoRoot, docsDir, tree.Under(docsDir))
	discoveredTasks, err := pluginsList[0].DiscoverTasks(t.Context(), docsDir, scope)
	if err != nil {
		t.Fatalf("DiscoverTasks failed: %v", err)
	}

	var buildTask tasks.Task
	for _, dt := range discoveredTasks {
		if dt.Name == "build" {
			buildTask = dt.Task
			break
		}
	}
	if buildTask == nil {
		t.Fatal("build task not found in discovered tasks")
	}

	if buildTask.GetName() != "mockplugin-build" {
		t.Errorf("expected task name 'mockplugin-build', got %q", buildTask.GetName())
	}

	if err := buildTask.Run(t.Context(), scope); err != nil {
		t.Fatalf("buildTask.Run failed: %v", err)
	}

	// Verify output was placed in docs/dist/output.html
	outputPath := filepath.Join(docsDir, "dist", "output.html")
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("expected output file %s: %v", outputPath, err)
	}
	if !strings.Contains(string(data), "built: hello world") {
		t.Errorf("unexpected output content: %q", string(data))
	}
}

func TestContainerTask_CoLocatedGenerate(t *testing.T) {
	imageRef := buildMockPluginImage(t)
	repoRoot, docsDir := setupTestWorkspace(t, imageRef)

	cacheDir := t.TempDir()
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	pluginsList, err := plugins.LoadContainerPlugins(t.Context(), repoRoot, cacheDir)
	if err != nil {
		t.Fatalf("LoadContainerPlugins failed: %v", err)
	}

	scope := tasks.NewTaskContext(repoRoot, docsDir, tree.Under(docsDir))
	discoveredTasks, err := pluginsList[0].DiscoverTasks(t.Context(), docsDir, scope)
	if err != nil {
		t.Fatalf("DiscoverTasks failed: %v", err)
	}

	var genTask tasks.Task
	for _, dt := range discoveredTasks {
		if dt.Name == "gen" {
			genTask = dt.Task
			break
		}
	}
	if genTask == nil {
		t.Fatal("gen task not found")
	}

	if err := genTask.Run(t.Context(), scope); err != nil {
		t.Fatalf("genTask.Run failed: %v", err)
	}

	// Verify co-located output in docs/generated.txt
	genPath := filepath.Join(docsDir, "generated.txt")
	data, err := os.ReadFile(genPath)
	if err != nil {
		t.Fatalf("expected co-located generated file %s: %v", genPath, err)
	}
	if !strings.Contains(string(data), "generated: hello world") {
		t.Errorf("unexpected content in %s: %q", genPath, string(data))
	}
}

func TestContainerTask_NetworkIsolation(t *testing.T) {
	imageRef := buildMockPluginImage(t)
	repoRoot, docsDir := setupTestWorkspace(t, imageRef)

	cacheDir := t.TempDir()
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	pluginsList, err := plugins.LoadContainerPlugins(t.Context(), repoRoot, cacheDir)
	if err != nil {
		t.Fatal(err)
	}

	scope := tasks.NewTaskContext(repoRoot, docsDir, tree.Under(docsDir))
	discoveredTasks, err := pluginsList[0].DiscoverTasks(t.Context(), docsDir, scope)
	if err != nil {
		t.Fatal(err)
	}

	var fetchTask tasks.Task
	for _, dt := range discoveredTasks {
		if dt.Name == "fetch" {
			fetchTask = dt.Task
			break
		}
	}
	if fetchTask == nil {
		t.Fatal("fetch task not found")
	}

	err = fetchTask.Run(t.Context(), scope)
	if err == nil {
		t.Fatal("expected fetch task to fail inside container running with --network none, but it succeeded")
	}
}

func TestContainerTask_VerifyMode(t *testing.T) {
	imageRef := buildMockPluginImage(t)
	repoRoot, docsDir := setupTestWorkspace(t, imageRef)

	cacheDir := t.TempDir()
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	pluginsList, err := plugins.LoadContainerPlugins(t.Context(), repoRoot, cacheDir)
	if err != nil {
		t.Fatal(err)
	}

	scope := tasks.NewTaskContext(repoRoot, docsDir, tree.Under(docsDir))
	discoveredTasks, err := pluginsList[0].DiscoverTasks(t.Context(), docsDir, scope)
	if err != nil {
		t.Fatal(err)
	}

	var genTask tasks.Task
	for _, dt := range discoveredTasks {
		if dt.Name == "gen" {
			genTask = dt.Task
			break
		}
	}
	if genTask == nil {
		t.Fatal("gen task not found")
	}

	genPath := filepath.Join(docsDir, "generated.txt")

	t.Run("fails when output differs and checkout is untouched", func(t *testing.T) {
		initialContent := "stale old content"
		if err := os.WriteFile(genPath, []byte(initialContent), 0644); err != nil {
			t.Fatal(err)
		}

		verifyCtx := plugins.WithVerify(t.Context(), true)
		err := genTask.Run(verifyCtx, scope)
		if err == nil {
			t.Fatal("expected verify to fail when repo file differs from plugin output")
		}

		// Ensure checkout file was untouched
		current, err := os.ReadFile(genPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(current) != initialContent {
			t.Errorf("checkout was modified during verify: got %q, want %q", string(current), initialContent)
		}
	})

	t.Run("passes when output matches and checkout is untouched", func(t *testing.T) {
		matchingContent := "generated: hello world\n"
		if err := os.WriteFile(genPath, []byte(matchingContent), 0644); err != nil {
			t.Fatal(err)
		}

		verifyCtx := plugins.WithVerify(t.Context(), true)
		if err := genTask.Run(verifyCtx, scope); err != nil {
			t.Fatalf("expected verify to pass when repo file matches output: %v", err)
		}

		current, err := os.ReadFile(genPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(current) != matchingContent {
			t.Errorf("checkout was modified: got %q, want %q", string(current), matchingContent)
		}
	})
}

func TestContainerTask_FailureOutputVisibleAndContainerCleanedUp(t *testing.T) {
	imageRef := buildMockPluginImage(t)
	repoRoot, docsDir := setupTestWorkspace(t, imageRef)

	cacheDir := t.TempDir()
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	pluginsList, err := plugins.LoadContainerPlugins(t.Context(), repoRoot, cacheDir)
	if err != nil {
		t.Fatal(err)
	}

	var errBuf bytes.Buffer
	scope := tasks.NewTaskContext(repoRoot, docsDir, tree.Under(docsDir)).WithOutput(nil, &errBuf)
	discoveredTasks, err := pluginsList[0].DiscoverTasks(t.Context(), docsDir, scope)
	if err != nil {
		t.Fatal(err)
	}

	var failTask tasks.Task
	for _, dt := range discoveredTasks {
		if dt.Name == "fail" {
			failTask = dt.Task
			break
		}
	}
	if failTask == nil {
		t.Fatal("fail task not found")
	}

	// Capture initial container count
	beforeOut, _ := exec.Command("docker", "ps", "-a", "--filter", "ancestor="+strings.Split(imageRef, "@")[0], "-q").Output()

	runErr := failTask.Run(t.Context(), scope)
	if runErr == nil {
		t.Fatal("expected failTask.Run to fail, but got nil")
	}

	// Output must be visible
	if !strings.Contains(errBuf.String(), "intentional plugin failure message") {
		t.Errorf("expected stderr to contain 'intentional plugin failure message', got: %s", errBuf.String())
	}

	// Container must be cleaned up
	afterOut, _ := exec.Command("docker", "ps", "-a", "--filter", "ancestor="+strings.Split(imageRef, "@")[0], "-q").Output()
	if string(beforeOut) != string(afterOut) {
		t.Errorf("container was left behind after failure: before %q, after %q", string(beforeOut), string(afterOut))
	}
}

func TestContainerTask_ContextCancelCleansUpContainer(t *testing.T) {
	imageRef := buildMockPluginImage(t)
	repoRoot, docsDir := setupTestWorkspace(t, imageRef)

	cacheDir := t.TempDir()
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	pluginsList, err := plugins.LoadContainerPlugins(t.Context(), repoRoot, cacheDir)
	if err != nil {
		t.Fatal(err)
	}

	scope := tasks.NewTaskContext(repoRoot, docsDir, tree.Under(docsDir))
	discoveredTasks, err := pluginsList[0].DiscoverTasks(t.Context(), docsDir, scope)
	if err != nil {
		t.Fatal(err)
	}

	var sleepTask tasks.Task
	for _, dt := range discoveredTasks {
		if dt.Name == "sleep" {
			sleepTask = dt.Task
			break
		}
	}
	if sleepTask == nil {
		t.Fatal("sleep task not found")
	}

	cancelCtx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(200*time.Millisecond, cancel)

	beforeOut, _ := exec.Command("docker", "ps", "-a", "--filter", "ancestor="+strings.Split(imageRef, "@")[0], "-q").Output()

	_ = sleepTask.Run(cancelCtx, scope)

	afterOut, _ := exec.Command("docker", "ps", "-a", "--filter", "ancestor="+strings.Split(imageRef, "@")[0], "-q").Output()
	if string(beforeOut) != string(afterOut) {
		t.Errorf("container was left behind after context cancellation: before %q, after %q", string(beforeOut), string(afterOut))
	}
}

func TestContainerTask_MissingRuntimeError(t *testing.T) {
	t.Setenv("CONTAINER_ENGINE", "nonexistent-runtime-999")
	t.Setenv("PATH", "")

	decl := config.PluginDeclaration{
		Image: "example.com/foo@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
	}
	manifest := &plugins.PluginManifest{
		Name: "test",
		Tasks: []plugins.TaskMetadata{
			{Name: "build", Verb: "build"},
		},
	}

	task := &plugins.ContainerTask{
		PluginName:  "test",
		TaskMeta:    manifest.Tasks[0],
		Declaration: decl,
		Manifest:    manifest,
		Dir:         t.TempDir(),
	}

	tree, _ := sourcetree.Open(task.Dir)
	scope := tasks.NewTaskContext(task.Dir, task.Dir, tree)

	err := task.Run(t.Context(), scope)
	if err == nil {
		t.Fatal("expected error when container runtime is missing")
	}

	if !strings.Contains(err.Error(), "requires a container runtime") {
		t.Errorf("expected error message to mention 'requires a container runtime', got: %v", err)
	}
}
