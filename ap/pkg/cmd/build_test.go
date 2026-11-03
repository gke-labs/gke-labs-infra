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
)

func TestRunBuild_NoImagesOrScriptsFails(t *testing.T) {
	tempDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(tempDir)

	opt := BuildOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoots:  []string{tempDir},
			DryRun:   true,
		},
	}

	err := RunBuild(t.Context(), opt)
	if err == nil {
		t.Fatal("expected RunBuild to fail when no images or build scripts are present under the root")
	}
	if !strings.Contains(err.Error(), "nothing to build under") {
		t.Errorf("expected error to contain 'nothing to build under', got: %v", err)
	}
}

func TestRunBuild_NoScopesFails(t *testing.T) {
	tempDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(tempDir)

	opt := BuildOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoots:  []string{},
			DryRun:   true,
		},
	}

	err := RunBuild(t.Context(), opt)
	if err == nil {
		t.Fatal("expected RunBuild to fail when no scopes are discovered")
	}
	if !strings.Contains(err.Error(), "no scopes found") {
		t.Errorf("expected error to contain 'no scopes found', got: %v", err)
	}
}

func TestRunBuild_WithImagesSucceeds(t *testing.T) {
	tempDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, "images", "myapp"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "images", "myapp", "Dockerfile"), []byte("FROM alpine\n"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(tempDir)

	opt := BuildOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoots:  []string{tempDir},
			DryRun:   true,
		},
	}

	if err := RunBuild(t.Context(), opt); err != nil {
		t.Fatalf("RunBuild failed: %v", err)
	}
}

func TestRunBuild_WithBuildScriptNoImagesSucceeds(t *testing.T) {
	tempDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	taskDir := filepath.Join(tempDir, "dev", "tasks")
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "build-foo"), []byte("#!/bin/sh\necho building foo\n"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(tempDir)

	opt := BuildOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoots:  []string{tempDir},
			DryRun:   true,
		},
	}

	if err := RunBuild(t.Context(), opt); err != nil {
		t.Fatalf("RunBuild failed for repository with build scripts and no images: %v", err)
	}
}

func TestNestedApBuild_BuildsNestedRepoImages(t *testing.T) {
	repoA := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoA, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repoA, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repoA, "images", "image-a"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoA, "images", "image-a", "Dockerfile"), []byte("FROM alpine\n"), 0644); err != nil {
		t.Fatal(err)
	}

	repoB := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoB, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repoB, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repoB, "images", "image-b"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoB, "images", "image-b", "Dockerfile"), []byte("FROM alpine\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Simulate running inside repo B while inheriting outer REPO_ROOT=repoA
	t.Setenv("REPO_ROOT", repoA)
	t.Setenv("AP_ROOT", "")
	t.Chdir(repoB)

	// Execute ap build via root cobra command to verify PersistentPreRunE and root resolution
	var execErr error
	output := captureStdout(t, func() {
		rootCmd := BuildRootCommand()
		rootCmd.SetArgs([]string{"--dry-run", "build"})
		execErr = rootCmd.ExecuteContext(t.Context())
	})

	if execErr != nil {
		t.Fatalf("ap build in nested repo failed: %v", execErr)
	}
	if !strings.Contains(output, "image-b") {
		t.Errorf("expected dry-run output to contain 'image-b', got:\n%s", output)
	}
	if strings.Contains(output, "image-a") {
		t.Errorf("expected dry-run output NOT to contain 'image-a', got:\n%s", output)
	}
}
