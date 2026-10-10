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

package golang

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

func TestHasGoFiles(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "hasgofiles-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name     string
		setup    func(root string) error
		expected bool
	}{
		{
			name: "empty directory",
			setup: func(root string) error {
				return nil
			},
			expected: false,
		},
		{
			name: "direct go file",
			setup: func(root string) error {
				return os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0644)
			},
			expected: true,
		},
		{
			name: "go file in subdirectory",
			setup: func(root string) error {
				dir := filepath.Join(root, "pkg")
				if err := os.Mkdir(dir, 0755); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package pkg"), 0644)
			},
			expected: true,
		},
		{
			name: "go file only in submodule",
			setup: func(root string) error {
				dir := filepath.Join(root, "submod")
				if err := os.Mkdir(dir, 0755); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module submod"), 0644); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package foo"), 0644)
			},
			expected: false,
		},
		{
			name: "go file in root and in submodule",
			setup: func(root string) error {
				if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0644); err != nil {
					return err
				}
				dir := filepath.Join(root, "submod")
				if err := os.Mkdir(dir, 0755); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module submod"), 0644); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package foo"), 0644)
			},
			expected: true,
		},
		{
			name: "non-go files only",
			setup: func(root string) error {
				return os.WriteFile(filepath.Join(root, "README.md"), []byte("# My Project"), 0644)
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(tmpDir, tt.name)
			if err := os.Mkdir(root, 0755); err != nil {
				t.Fatalf("Failed to create test root: %v", err)
			}
			if err := tt.setup(root); err != nil {
				t.Fatalf("Failed to setup test: %v", err)
			}

			got, err := hasGoFiles(root)
			if err != nil {
				t.Errorf("hasGoFiles() error = %v", err)
				return
			}
			if got != tt.expected {
				t.Errorf("hasGoFiles() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestLintTasks_DroppedErrors(t *testing.T) {
	setupModule := func(t *testing.T, apYAML string) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.27\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if apYAML != "" {
			apDir := filepath.Join(dir, ".ap")
			if err := os.MkdirAll(apDir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(apYAML), 0644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}

	findDroppedTask := func(taskGroup tasks.Task) *DroppedErrorsCheckTask {
		group, ok := taskGroup.(*tasks.Group)
		if !ok {
			return nil
		}
		for _, t := range group.Tasks {
			if modGroup, ok := t.(*tasks.Group); ok {
				for _, sub := range modGroup.Tasks {
					if dt, ok := sub.(*DroppedErrorsCheckTask); ok {
						return dt
					}
				}
			}
		}
		return nil
	}

	t.Run("default_error", func(t *testing.T) {
		dir := setupModule(t, "")
		taskGroup, err := LintTasks(dir)
		if err != nil {
			t.Fatal(err)
		}
		dt := findDroppedTask(taskGroup)
		if dt == nil {
			t.Fatalf("expected DroppedErrorsCheckTask to be included by default")
		}
		if dt.IsError != true {
			t.Errorf("expected IsError to be true in default error mode")
		}
	})

	t.Run("mode_warn", func(t *testing.T) {
		dir := setupModule(t, "lint:\n  droppedErrors:\n    mode: warn\n")
		taskGroup, err := LintTasks(dir)
		if err != nil {
			t.Fatal(err)
		}
		dt := findDroppedTask(taskGroup)
		if dt == nil {
			t.Fatalf("expected DroppedErrorsCheckTask to be included")
		}
		if dt.IsError != false {
			t.Errorf("expected IsError to be false in mode warn")
		}
	})

	t.Run("mode_error", func(t *testing.T) {
		dir := setupModule(t, "lint:\n  droppedErrors:\n    mode: error\n")
		taskGroup, err := LintTasks(dir)
		if err != nil {
			t.Fatal(err)
		}
		dt := findDroppedTask(taskGroup)
		if dt == nil {
			t.Fatalf("expected DroppedErrorsCheckTask to be included")
		}
		if dt.IsError != true {
			t.Errorf("expected IsError to be true in mode error")
		}
	})

	t.Run("mode_ignore", func(t *testing.T) {
		dir := setupModule(t, "lint:\n  droppedErrors:\n    mode: ignore\n")
		taskGroup, err := LintTasks(dir)
		if err != nil {
			t.Fatal(err)
		}
		dt := findDroppedTask(taskGroup)
		if dt != nil {
			t.Fatalf("expected DroppedErrorsCheckTask to be excluded in mode ignore")
		}
	})
}

func TestLintTasks_SkipGenerated(t *testing.T) {
	setupModule := func(t *testing.T, apYAML string) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.27\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if apYAML != "" {
			apDir := filepath.Join(dir, ".ap")
			if err := os.MkdirAll(apDir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(apYAML), 0644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}

	findTasks := func(taskGroup tasks.Task) (*DroppedErrorsCheckTask, *TestContextCheckTask) {
		group, ok := taskGroup.(*tasks.Group)
		if !ok {
			return nil, nil
		}
		var dt *DroppedErrorsCheckTask
		var tc *TestContextCheckTask
		for _, t := range group.Tasks {
			if modGroup, ok := t.(*tasks.Group); ok {
				for _, sub := range modGroup.Tasks {
					if d, ok := sub.(*DroppedErrorsCheckTask); ok {
						dt = d
					}
					if c, ok := sub.(*TestContextCheckTask); ok {
						tc = c
					}
				}
			}
		}
		return dt, tc
	}

	t.Run("skipGenerated_true", func(t *testing.T) {
		dir := setupModule(t, "lint:\n  skipGenerated: true\n")
		taskGroup, err := LintTasks(dir)
		if err != nil {
			t.Fatal(err)
		}
		dt, tc := findTasks(taskGroup)
		if dt == nil || tc == nil {
			t.Fatalf("expected tasks to be found")
		}
		if !dt.SkipGenerated {
			t.Errorf("expected DroppedErrorsCheckTask.SkipGenerated to be true")
		}
		if !tc.SkipGenerated {
			t.Errorf("expected TestContextCheckTask.SkipGenerated to be true")
		}
	})

	t.Run("default_skipGenerated", func(t *testing.T) {
		dir := setupModule(t, "")
		taskGroup, err := LintTasks(dir)
		if err != nil {
			t.Fatal(err)
		}
		dt, tc := findTasks(taskGroup)
		if dt == nil || tc == nil {
			t.Fatalf("expected tasks to be found")
		}
		if dt.SkipGenerated {
			t.Errorf("expected DroppedErrorsCheckTask.SkipGenerated to be false by default")
		}
		if tc.SkipGenerated {
			t.Errorf("expected TestContextCheckTask.SkipGenerated to be false by default")
		}
	})
}
