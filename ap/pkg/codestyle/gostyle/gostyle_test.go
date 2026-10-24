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

package gostyle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/repo"
)

func TestRun_NoConfig(t *testing.T) {
	// Create a temporary directory for the mock repo
	tmpDir, err := os.MkdirTemp("", "gostyle-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	ctx := t.Context()

	// Run should succeed (do nothing) when no config exists
	r, err := repo.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	err = Run(ctx, r, tmpDir, nil)
	if err != nil {
		t.Errorf("Expected success from Run (no config), got error: %v", err)
	}
}

func TestRun_WithConfig(t *testing.T) {
	// Create a temporary directory for the mock repo
	tmpDir, err := os.MkdirTemp("", "gostyle-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create .ap directory
	apDir := filepath.Join(tmpDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatalf("Failed to create .ap dir: %v", err)
	}

	// Create go.yaml
	configContent := `
gofmt:
  enabled: true
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write go.yaml: %v", err)
	}

	// Create a badly formatted go file
	badContent := "package main\nfunc main() {\nprintln(\"hello\")\n}\n"
	badFile := filepath.Join(tmpDir, "bad.go")
	if err := os.WriteFile(badFile, []byte(badContent), 0644); err != nil {
		t.Fatalf("Failed to write bad.go: %v", err)
	}

	ctx := t.Context()
	r, err := repo.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, r, tmpDir, nil); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Check if formatted
	formattedContent, err := os.ReadFile(badFile)
	if err != nil {
		t.Fatalf("Failed to read bad.go: %v", err)
	}

	expected := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	if string(formattedContent) != expected {
		t.Errorf("File was not formatted correctly.\nGot:\n%s\nExpected:\n%s", string(formattedContent), expected)
	}
}

func TestRun_GoModTidy_UntidyModuleMadeTidy(t *testing.T) {
	tmpDir := t.TempDir()

	// Create an untidy go.mod with an unused requirement
	goModContent := "module example.com/testmod\n\ngo 1.27\n\nrequire example.com/unused v1.0.0\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goModContent), 0644); err != nil {
		t.Fatal(err)
	}

	mainContent := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(mainContent), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	r, err := repo.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	if err := Run(ctx, r, tmpDir, nil); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	updatedMod, err := os.ReadFile(filepath.Join(tmpDir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(updatedMod), "require example.com/unused") {
		t.Errorf("expected unused requirement to be removed by go mod tidy, got:\n%s", string(updatedMod))
	}
}

func TestRun_GoModTidy_MultiModuleWithGoWork(t *testing.T) {
	tmpDir := t.TempDir()

	// Root module
	rootMod := "module example.com/root\n\ngo 1.27\n\nrequire example.com/unusedroot v1.0.0\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(rootMod), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Submodule
	subDir := filepath.Join(tmpDir, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	subMod := "module example.com/root/sub\n\ngo 1.27\n\nrequire example.com/unusedsub v1.0.0\n"
	if err := os.WriteFile(filepath.Join(subDir, "go.mod"), []byte(subMod), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "sub.go"), []byte("package sub\nfunc Sub() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// go.work at root
	goWork := "go 1.27\n\nuse (\n\t.\n\t./sub\n)\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "go.work"), []byte(goWork), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	r, err := repo.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	if err := Run(ctx, r, tmpDir, nil); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	rootModAfter, err := os.ReadFile(filepath.Join(tmpDir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rootModAfter), "require example.com/unusedroot") {
		t.Errorf("expected root module unused require removed, got:\n%s", string(rootModAfter))
	}

	subModAfter, err := os.ReadFile(filepath.Join(subDir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(subModAfter), "require example.com/unusedsub") {
		t.Errorf("expected sub module unused require removed, got:\n%s", string(subModAfter))
	}
}

func TestRun_GoModTidy_ChangeCache(t *testing.T) {
	tmpDir := t.TempDir()

	goMod := "module example.com/cachetest\n\ngo 1.27\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}
	mainGo := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(mainGo), 0644); err != nil {
		t.Fatal(err)
	}

	// Spy on tidyRunner
	runCount := 0
	origTidyRunner := tidyRunner
	defer func() { tidyRunner = origTidyRunner }()

	tidyRunner = func(ctx context.Context, r *repo.Repo, dir string) error {
		runCount++
		return origTidyRunner(ctx, r, dir)
	}

	cacheDir := t.TempDir()
	t.Setenv("AP_CACHE_DIR", cacheDir)

	ctx := t.Context()
	r, err := repo.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	// First run: should execute tidy
	if err := Run(ctx, r, tmpDir, nil); err != nil {
		t.Fatalf("first Run failed: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("expected runCount=1 on initial run, got %d", runCount)
	}

	// Second run with no changes: should skip tidy due to cache
	if err := Run(ctx, r, tmpDir, nil); err != nil {
		t.Fatalf("second Run failed: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("expected runCount to remain 1 on unchanged run, got %d", runCount)
	}

	// Modify a non-Go file (README.md): should still skip tidy
	if err := os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# Notes\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, r, tmpDir, nil); err != nil {
		t.Fatalf("third Run (after README change) failed: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("expected runCount to remain 1 after non-Go file modified, got %d", runCount)
	}

	// Modify a Go file: should re-tidy!
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n\nfunc main() { /* edited */ }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, r, tmpDir, nil); err != nil {
		t.Fatalf("fourth Run (after Go file change) failed: %v", err)
	}
	if runCount != 2 {
		t.Fatalf("expected runCount to be 2 after Go file modified, got %d", runCount)
	}
}

func TestRun_GoModTidy_NetworkFailure(t *testing.T) {
	tmpDir := t.TempDir()

	// Create module importing non-existent remote package
	goMod := "module example.com/failmod\n\ngo 1.27\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}
	mainGo := "package main\n\nimport _ \"example.com/nonexistent/package/that/cannot/be/resolved\"\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(mainGo), 0644); err != nil {
		t.Fatal(err)
	}

	// Set GOPROXY=off to simulate offline / network failure
	t.Setenv("GOPROXY", "off")

	ctx := t.Context()
	r, err := repo.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	err = Run(ctx, r, tmpDir, nil)
	if err == nil {
		t.Fatal("expected Run to fail on unresolved dependency with GOPROXY=off, got nil")
	}

	if !strings.Contains(err.Error(), "go mod tidy failed") {
		t.Errorf("expected error message to contain 'go mod tidy failed', got: %v", err)
	}
}

func TestRun_GoModTidy_DisabledInConfig(t *testing.T) {
	tmpDir := t.TempDir()

	apDir := filepath.Join(tmpDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	configContent := "tidy:\n  enabled: false\n"
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	goModContent := "module example.com/testmod\n\ngo 1.27\n\nrequire example.com/unused v1.0.0\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goModContent), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	r, err := repo.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	if err := Run(ctx, r, tmpDir, nil); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Verify go.mod was NOT modified
	data, err := os.ReadFile(filepath.Join(tmpDir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "require example.com/unused v1.0.0") {
		t.Errorf("expected go.mod to remain unchanged when tidy is disabled, got:\n%s", string(data))
	}
}

func TestRun_GoModTidy_SkipPattern(t *testing.T) {
	tmpDir := t.TempDir()

	apDir := filepath.Join(tmpDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	configContent := "skip:\n  - skipped/**\n"
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	skippedDir := filepath.Join(tmpDir, "skipped")
	if err := os.Mkdir(skippedDir, 0755); err != nil {
		t.Fatal(err)
	}
	goModContent := "module example.com/skipped\n\ngo 1.27\n\nrequire example.com/unused v1.0.0\n"
	if err := os.WriteFile(filepath.Join(skippedDir, "go.mod"), []byte(goModContent), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	r, err := repo.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	if err := Run(ctx, r, tmpDir, nil); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Verify skipped/go.mod was NOT modified
	data, err := os.ReadFile(filepath.Join(skippedDir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "require example.com/unused v1.0.0") {
		t.Errorf("expected skipped/go.mod to remain unchanged, got:\n%s", string(data))
	}
}
