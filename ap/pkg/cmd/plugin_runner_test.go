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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
)

var (
	cmdMockImageOnce sync.Once
	cmdMockImageRef  string
	cmdMockImageErr  error
)

func getCmdMockPluginImage(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found, skipping container plugin test")
	}

	cmdMockImageOnce.Do(func() {
		cmdMockImageRef, cmdMockImageErr = buildMockPluginImageForCmd()
	})
	if cmdMockImageErr != nil {
		t.Fatalf("failed to build test plugin image: %v", cmdMockImageErr)
	}
	return cmdMockImageRef
}

func buildMockPluginImageForCmd() (string, error) {
	tempDir, err := os.MkdirTemp("", "ap-mock-cmd-*")
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
  "name": "astro",
  "activation": ["astro.config.mjs"],
  "network": false,
  "tasks": [
    {
      "name": "build",
      "verb": "build",
      "description": "Build documentation site",
      "outputs": ["dist"]
    },
    {
      "name": "codegen",
      "verb": "generate",
      "description": "Generate co-located files",
      "outputs": ["."]
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
        echo "site built from $(cat "$SRC/content.md")" > "$OUT/dist/index.html"
        ;;
      codegen)
        echo "generated from $(cat "$SRC/content.md")" > "$OUT/generated.txt"
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

	tag := fmt.Sprintf("ap-mock-cmd-astro:%d", time.Now().UnixNano())
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

func setupCmdWorkspace(t *testing.T, imageRef string) (string, string) {
	t.Helper()
	repoRoot := t.TempDir()

	files := map[string]string{
		".git/config":           "",
		"docs/astro.config.mjs": "export default {}",
		"docs/content.md":       "welcome to docs",
		".ap/plugins.yaml": fmt.Sprintf(`plugins:
  astro:
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

	return repoRoot, filepath.Join(repoRoot, "docs")
}

func TestRunBuild_ActivatedContainerPlugin(t *testing.T) {
	sourcetree.ResetCache()
	imageRef := getCmdMockPluginImage(t)
	repoRoot, docsDir := setupCmdWorkspace(t, imageRef)

	t.Chdir(repoRoot)

	rootOpt := &RootOptions{
		RepoRoot: repoRoot,
		Dir:      repoRoot,
	}
	buildOpt := BuildOptions{
		RootOptions: rootOpt,
	}

	if err := RunBuild(t.Context(), buildOpt); err != nil {
		t.Fatalf("RunBuild failed: %v", err)
	}

	distFile := filepath.Join(docsDir, "dist", "index.html")
	content, err := os.ReadFile(distFile)
	if err != nil {
		t.Fatalf("expected output file %s: %v", distFile, err)
	}

	if !strings.Contains(string(content), "site built from welcome to docs") {
		t.Errorf("unexpected content in %s: %q", distFile, string(content))
	}
}

func TestRunGenerate_ActivatedContainerPlugin(t *testing.T) {
	sourcetree.ResetCache()
	imageRef := getCmdMockPluginImage(t)
	repoRoot, docsDir := setupCmdWorkspace(t, imageRef)

	t.Chdir(repoRoot)

	rootOpt := &RootOptions{
		RepoRoot: repoRoot,
		Dir:      repoRoot,
	}
	genOpt := GenerateOptions{
		RootOptions: rootOpt,
	}

	if err := RunGenerate(t.Context(), genOpt); err != nil {
		t.Fatalf("RunGenerate failed: %v", err)
	}

	genFile := filepath.Join(docsDir, "generated.txt")
	content, err := os.ReadFile(genFile)
	if err != nil {
		t.Fatalf("expected generated file %s: %v", genFile, err)
	}
	expected := "generated from welcome to docs\n"
	if string(content) != expected {
		t.Errorf("got %q, want %q", string(content), expected)
	}

	// Test verify mode passes when output matches
	verifyOpt := GenerateOptions{
		RootOptions: rootOpt,
		Verify:      true,
	}
	if err := RunGenerate(t.Context(), verifyOpt); err != nil {
		t.Fatalf("RunGenerate --verify failed when matching: %v", err)
	}

	// Mutate file and assert verify mode fails without touching checkout
	mutated := "manually modified content"
	if err := os.WriteFile(genFile, []byte(mutated), 0644); err != nil {
		t.Fatal(err)
	}

	if err := RunGenerate(t.Context(), verifyOpt); err == nil {
		t.Fatal("expected RunGenerate --verify to fail on differing file")
	}

	// Verify file was NOT modified by verify mode
	afterVerify, err := os.ReadFile(genFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterVerify) != mutated {
		t.Errorf("file modified during verify: got %q, want %q", string(afterVerify), mutated)
	}
}
