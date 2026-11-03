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
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
)

func TestBuildGenerateCommand(t *testing.T) {
	rootOpt := &RootOptions{}
	cmd := BuildGenerateCommand(rootOpt)

	if cmd.Use != "generate [path...]" {
		t.Errorf("unexpected Use: %q", cmd.Use)
	}

	verifyFlag := cmd.Flags().Lookup("verify")
	if verifyFlag == nil {
		t.Fatalf("expected --verify flag to exist")
	}
	if verifyFlag.DefValue != "false" {
		t.Errorf("expected --verify default to be false, got %q", verifyFlag.DefValue)
	}

	auditFlag := cmd.Flags().Lookup("audit-generated")
	if auditFlag == nil {
		t.Fatalf("expected --audit-generated flag to exist")
	}
	if auditFlag.DefValue != "false" {
		t.Errorf("expected --audit-generated default to be false, got %q", auditFlag.DefValue)
	}

	strictFlag := cmd.Flags().Lookup("strict")
	if strictFlag == nil {
		t.Fatalf("expected --strict flag to exist")
	}
	if strictFlag.DefValue != "false" {
		t.Errorf("expected --strict default to be false, got %q", strictFlag.DefValue)
	}
}

func TestBuildVerifyGenerateCommand(t *testing.T) {
	rootOpt := &RootOptions{}
	cmd := BuildVerifyGenerateCommand(rootOpt)

	if cmd.Use != "verify-generate [path...]" {
		t.Errorf("unexpected Use: %q", cmd.Use)
	}
}

func TestAuditGeneratedDryRun(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apDir, "ap.yaml"), []byte("version: latest\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apDir, "headers.yaml"), []byte("license: apache-2.0\ncopyrightHolder: Google LLC\n"), 0644); err != nil {
		t.Fatal(err)
	}

	rootOpt := &RootOptions{
		RepoRoot: tempDir,
		Dir:      tempDir,
		DryRun:   true,
	}

	opt := GenerateOptions{
		RootOptions:    rootOpt,
		AuditGenerated: true,
	}

	if err := opt.Resolve(nil); err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if err := RunGenerate(t.Context(), opt); err != nil {
		t.Fatalf("RunGenerate in audit dry-run mode failed: %v", err)
	}
}

func TestVerifyGenerateDryRun(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apDir, "ap.yaml"), []byte("version: latest\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apDir, "headers.yaml"), []byte("license: apache-2.0\ncopyrightHolder: Google LLC\n"), 0644); err != nil {
		t.Fatal(err)
	}

	rootOpt := &RootOptions{
		RepoRoot: tempDir,
		Dir:      tempDir,
		DryRun:   true,
	}

	opt := GenerateOptions{
		RootOptions: rootOpt,
		Verify:      true,
	}

	if err := opt.Resolve(nil); err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if err := RunGenerate(t.Context(), opt); err != nil {
		t.Fatalf("RunGenerate in verify dry-run mode failed: %v", err)
	}
}

func TestRunGenerate_UntidyGoMod(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apDir, "ap.yaml"), []byte("version: latest\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apDir, "headers.yaml"), []byte("license: apache-2.0\ncopyrightHolder: Google LLC\n"), 0644); err != nil {
		t.Fatal(err)
	}

	goModContent := "module example.com/gentest\n\ngo 1.27\n\nrequire example.com/unused v1.0.0\n"
	if err := os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte(goModContent), 0644); err != nil {
		t.Fatal(err)
	}
	mainGoContent := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(tempDir, "main.go"), []byte(mainGoContent), 0644); err != nil {
		t.Fatal(err)
	}

	rootOpt := &RootOptions{
		RepoRoot: tempDir,
		Dir:      tempDir,
	}
	opt := GenerateOptions{
		RootOptions: rootOpt,
	}
	if err := opt.Resolve(nil); err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if err := RunGenerate(t.Context(), opt); err != nil {
		t.Fatalf("RunGenerate failed: %v", err)
	}

	updatedMod, err := os.ReadFile(filepath.Join(tempDir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(updatedMod), "require example.com/unused") {
		t.Errorf("expected ap generate to tidy go.mod and remove unused requirement, got:\n%s", string(updatedMod))
	}
}

func gitEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_CONFIG_") {
			env = append(env, e)
		}
	}
	return env
}

func TestRunGenerate_VerifyReportsUntidyGoMod(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	tempDir := t.TempDir()

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tempDir
	cmd.Env = gitEnv()
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	for _, args := range [][]string{
		{"config", "user.name", "Test User"},
		{"config", "user.email", "test@example.com"},
	} {
		c := exec.Command("git", args...)
		c.Dir = tempDir
		c.Env = gitEnv()
		if err := c.Run(); err != nil {
			t.Fatalf("git %v failed: %v", args, err)
		}
	}

	apDir := filepath.Join(tempDir, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apDir, "ap.yaml"), []byte("version: latest\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apDir, "headers.yaml"), []byte("license: apache-2.0\ncopyrightHolder: Google LLC\n"), 0644); err != nil {
		t.Fatal(err)
	}

	goModContent := "module example.com/gentest\n\ngo 1.27\n\nrequire example.com/unused v1.0.0\n"
	if err := os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte(goModContent), 0644); err != nil {
		t.Fatal(err)
	}
	mainGoContent := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(tempDir, "main.go"), []byte(mainGoContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Commit untidy go.mod
	addCmd := exec.Command("git", "add", ".")
	addCmd.Dir = tempDir
	addCmd.Env = gitEnv()
	if err := addCmd.Run(); err != nil {
		t.Fatalf("git add failed: %v", err)
	}
	commitCmd := exec.Command("git", "commit", "-m", "initial commit with untidy go.mod")
	commitCmd.Dir = tempDir
	commitCmd.Env = gitEnv()
	if err := commitCmd.Run(); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	rootOpt := &RootOptions{
		RepoRoot: tempDir,
		Dir:      tempDir,
	}
	opt := GenerateOptions{
		RootOptions: rootOpt,
		Verify:      true,
	}
	if err := opt.Resolve(nil); err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	err := RunGenerate(t.Context(), opt)
	if err == nil {
		t.Fatal("expected RunGenerate with --verify to fail on untidy go.mod, got nil")
	}
	if !strings.Contains(err.Error(), "working tree was dirtied by generation") || !strings.Contains(err.Error(), "go.mod") {
		t.Errorf("expected error to report working tree dirtied with go.mod, got: %v", err)
	}
}

func TestRunGenerate_SecondRunOpensNoUnchangedFiles(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := t.TempDir()
	t.Setenv("AP_CACHE_DIR", cacheDir)

	apDir := filepath.Join(tempDir, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apDir, "ap.yaml"), []byte("version: latest\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apDir, "headers.yaml"), []byte("license: apache-2.0\ncopyrightHolder: Google LLC\n"), 0644); err != nil {
		t.Fatal(err)
	}

	goModContent := "module example.com/gentest\n\ngo 1.27\n"
	if err := os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte(goModContent), 0644); err != nil {
		t.Fatal(err)
	}
	mainGoContent := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(tempDir, "main.go"), []byte(mainGoContent), 0644); err != nil {
		t.Fatal(err)
	}

	// First run: cold cache
	sourcetree.ResetCache()
	sourcetree.ResetContentReadCount()

	rootOpt1 := &RootOptions{
		RepoRoot: tempDir,
		Dir:      tempDir,
	}
	opt1 := GenerateOptions{RootOptions: rootOpt1}
	if err := opt1.Resolve(nil); err != nil {
		t.Fatalf("Resolve 1 failed: %v", err)
	}
	if err := RunGenerate(t.Context(), opt1); err != nil {
		t.Fatalf("RunGenerate 1 failed: %v", err)
	}

	// Verify cold run performed some content reads
	if sourcetree.ContentReadCount() == 0 {
		t.Errorf("expected cold run to open and read files for hashing, got 0")
	}

	// Second run: warm cache
	sourcetree.ResetCache()
	sourcetree.ResetContentReadCount()

	rootOpt2 := &RootOptions{
		RepoRoot: tempDir,
		Dir:      tempDir,
	}
	opt2 := GenerateOptions{RootOptions: rootOpt2}
	if err := opt2.Resolve(nil); err != nil {
		t.Fatalf("Resolve 2 failed: %v", err)
	}
	if err := RunGenerate(t.Context(), opt2); err != nil {
		t.Fatalf("RunGenerate 2 failed: %v", err)
	}

	// Second generate should open NO unchanged files for hashing
	if count := sourcetree.ContentReadCount(); count != 0 {
		t.Errorf("expected second ap generate to perform 0 content reads for unchanged files, got %d", count)
	}

	// Check cache directory: only done.bin and hashes.bin should exist
	codestyleDir := filepath.Join(cacheDir, "codestyle")
	entries, err := os.ReadDir(codestyleDir)
	if err != nil {
		t.Fatalf("failed to read codestyle cache dir: %v", err)
	}

	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)

	expectedNames := []string{"done.bin", "hashes.bin"}
	if !slices.Equal(names, expectedNames) {
		t.Errorf("expected codestyle cache to contain only %v, got %v", expectedNames, names)
	}
}
