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
			Dir:      tempDir,
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
			Dir:      filepath.Join(tempDir, "nonexistent"),
			DryRun:   true,
		},
	}

	err := RunBuild(t.Context(), opt)
	if err == nil {
		t.Fatal("expected RunBuild to fail when no scopes are discovered")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("expected error to contain 'does not exist', got: %v", err)
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
			Dir:      tempDir,
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
			Dir:      tempDir,
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

func TestRunBuild_ImageDirectoryTargets(t *testing.T) {
	tempDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}

	for _, img := range []string{"a", "b", "wal-buffer", "objectfs-controller"} {
		dir := filepath.Join(tempDir, "images", img)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	subDir := filepath.Join(tempDir, "sub", "images", "c")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "Dockerfile"), []byte("FROM alpine\n"), 0644); err != nil {
		t.Fatal(err)
	}

	notAnImageDir := filepath.Join(tempDir, "not-an-image")
	if err := os.MkdirAll(notAnImageDir, 0755); err != nil {
		t.Fatal(err)
	}

	scriptDir1 := filepath.Join(tempDir, "subtasks1", "dev", "tasks")
	if err := os.MkdirAll(scriptDir1, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir1, "build-tool"), []byte("#!/bin/sh\necho tool 1\n"), 0755); err != nil {
		t.Fatal(err)
	}

	scriptDir2 := filepath.Join(tempDir, "subtasks2", "dev", "tasks")
	if err := os.MkdirAll(scriptDir2, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir2, "build-tool"), []byte("#!/bin/sh\necho tool 2\n"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(tempDir)

	t.Run("single image directory target plans only that image", func(t *testing.T) {
		var execErr error
		output := captureStdout(t, func() {
			rootCmd := BuildRootCommand()
			rootCmd.SetArgs([]string{"--dry-run", "build", "images/a"})
			execErr = rootCmd.ExecuteContext(t.Context())
		})
		if execErr != nil {
			t.Fatalf("ap build images/a failed: %v", execErr)
		}
		if !strings.Contains(output, "docker-build-a") {
			t.Errorf("expected output to contain docker-build-a, got:\n%s", output)
		}
		for _, excluded := range []string{"docker-build-b", "docker-build-c", "docker-build-wal-buffer", "docker-build-objectfs-controller"} {
			if strings.Contains(output, excluded) {
				t.Errorf("expected output NOT to contain %s, got:\n%s", excluded, output)
			}
		}
	})

	t.Run("mixing image target and subtree target plans union", func(t *testing.T) {
		var execErr error
		output := captureStdout(t, func() {
			rootCmd := BuildRootCommand()
			rootCmd.SetArgs([]string{"--dry-run", "build", "images/a", "sub/"})
			execErr = rootCmd.ExecuteContext(t.Context())
		})
		if execErr != nil {
			t.Fatalf("ap build images/a sub/ failed: %v", execErr)
		}
		if !strings.Contains(output, "docker-build-a") {
			t.Errorf("expected output to contain docker-build-a, got:\n%s", output)
		}
		if !strings.Contains(output, "docker-build-c") {
			t.Errorf("expected output to contain docker-build-c, got:\n%s", output)
		}
		for _, excluded := range []string{"docker-build-b", "docker-build-wal-buffer", "docker-build-objectfs-controller"} {
			if strings.Contains(output, excluded) {
				t.Errorf("expected output NOT to contain %s, got:\n%s", excluded, output)
			}
		}
	})

	t.Run("target that is not an image directory still errors", func(t *testing.T) {
		var execErr error
		captureStdout(t, func() {
			rootCmd := BuildRootCommand()
			rootCmd.SetArgs([]string{"--dry-run", "build", "not-an-image"})
			execErr = rootCmd.ExecuteContext(t.Context())
		})
		if execErr == nil {
			t.Fatal("expected build of non-image directory without tasks to error, got nil")
		}
		if !strings.Contains(execErr.Error(), "nothing to build under") {
			t.Errorf("expected error to contain 'nothing to build under', got: %v", execErr)
		}
	})

	t.Run("nonexistent target errors with does not exist", func(t *testing.T) {
		var execErr error
		captureStdout(t, func() {
			rootCmd := BuildRootCommand()
			rootCmd.SetArgs([]string{"--dry-run", "build", "images/nope"})
			execErr = rootCmd.ExecuteContext(t.Context())
		})
		if execErr == nil {
			t.Fatal("expected build of nonexistent target to error, got nil")
		}
		if !strings.Contains(execErr.Error(), `target path "images/nope" does not exist`) {
			t.Errorf("expected error to contain 'target path \"images/nope\" does not exist', got: %v", execErr)
		}
	})

	t.Run("two same-named build scripts under different targets both plan", func(t *testing.T) {
		var execErr error
		output := captureStdout(t, func() {
			rootCmd := BuildRootCommand()
			rootCmd.SetArgs([]string{"--dry-run", "build", "subtasks1", "subtasks2"})
			execErr = rootCmd.ExecuteContext(t.Context())
		})
		if execErr != nil {
			t.Fatalf("ap build subtasks1 subtasks2 failed: %v", execErr)
		}
		if count := strings.Count(output, "build-tool"); count != 2 {
			t.Errorf("expected 2 'build-tool' tasks in dry-run output, got %d:\n%s", count, output)
		}

		// Also verify plain ap build at root plans both
		rootOutput := captureStdout(t, func() {
			rootCmd := BuildRootCommand()
			rootCmd.SetArgs([]string{"--dry-run", "build"})
			execErr = rootCmd.ExecuteContext(t.Context())
		})
		if execErr != nil {
			t.Fatalf("plain ap build failed: %v", execErr)
		}
		if count := strings.Count(rootOutput, "build-tool"); count != 2 {
			t.Errorf("expected 2 'build-tool' tasks in root dry-run output, got %d:\n%s", count, rootOutput)
		}

		// And verify duplicate identical targets plan only one
		dupOutput := captureStdout(t, func() {
			rootCmd := BuildRootCommand()
			rootCmd.SetArgs([]string{"--dry-run", "build", "subtasks1", "subtasks1"})
			execErr = rootCmd.ExecuteContext(t.Context())
		})
		if execErr != nil {
			t.Fatalf("ap build subtasks1 subtasks1 failed: %v", execErr)
		}
		if count := strings.Count(dupOutput, "build-tool"); count != 1 {
			t.Errorf("expected 1 'build-tool' task for duplicate targets, got %d:\n%s", count, dupOutput)
		}
	})

	t.Run("multiple image directory targets build exactly those images", func(t *testing.T) {
		var execErr error
		output := captureStdout(t, func() {
			rootCmd := BuildRootCommand()
			rootCmd.SetArgs([]string{"--dry-run", "build", "images/wal-buffer", "images/objectfs-controller"})
			execErr = rootCmd.ExecuteContext(t.Context())
		})
		if execErr != nil {
			t.Fatalf("ap build failed: %v", execErr)
		}
		if !strings.Contains(output, "docker-build-wal-buffer") {
			t.Errorf("expected output to contain docker-build-wal-buffer, got:\n%s", output)
		}
		if !strings.Contains(output, "docker-build-objectfs-controller") {
			t.Errorf("expected output to contain docker-build-objectfs-controller, got:\n%s", output)
		}
		for _, excluded := range []string{"docker-build-a", "docker-build-b", "docker-build-c"} {
			if strings.Contains(output, excluded) {
				t.Errorf("expected output NOT to contain %s, got:\n%s", excluded, output)
			}
		}
	})
}
