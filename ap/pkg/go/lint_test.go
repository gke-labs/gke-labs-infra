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
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
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

			r, err := sourcetree.Open(root)
			if err != nil {
				t.Fatalf("sourcetree.Open failed: %v", err)
			}
			got, err := hasGoFiles(r, root)
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
	setupModule := func(t *testing.T, apYAML string) (string, *sourcetree.SourceTree) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.27\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644); err != nil {
			t.Fatal(err)
		}
		var skips []string
		if apYAML != "" {
			apDir := filepath.Join(dir, ".ap")
			if err := os.MkdirAll(apDir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(apYAML), 0644); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			skips = cfg.Skip
		}
		r, err := sourcetree.Open(dir, sourcetree.WithSkip(skips...))
		if err != nil {
			t.Fatal(err)
		}
		return dir, r
	}

	t.Run("default_error", func(t *testing.T) {
		dir, r := setupModule(t, "")
		taskGroup, err := LintTasks(r, dir)
		if err != nil {
			t.Fatal(err)
		}
		at := findAnalyzersTask(taskGroup)
		if at == nil {
			t.Fatalf("expected AnalyzersTask to be included by default")
		}
		if at.Enabled["droppederrors"] != codestyle.ModeError {
			t.Errorf("expected droppederrors to be ModeError in default, got %v", at.Enabled["droppederrors"])
		}
	})

	t.Run("mode_warn", func(t *testing.T) {
		dir, r := setupModule(t, "lint:\n  droppedErrors:\n    mode: warn\n")
		taskGroup, err := LintTasks(r, dir)
		if err != nil {
			t.Fatal(err)
		}
		at := findAnalyzersTask(taskGroup)
		if at == nil {
			t.Fatalf("expected AnalyzersTask to be included")
		}
		if at.Enabled["droppederrors"] != codestyle.ModeWarn {
			t.Errorf("expected droppederrors to be ModeWarn, got %v", at.Enabled["droppederrors"])
		}
	})

	t.Run("mode_error", func(t *testing.T) {
		dir, r := setupModule(t, "lint:\n  droppedErrors:\n    mode: error\n")
		taskGroup, err := LintTasks(r, dir)
		if err != nil {
			t.Fatal(err)
		}
		at := findAnalyzersTask(taskGroup)
		if at == nil {
			t.Fatalf("expected AnalyzersTask to be included")
		}
		if at.Enabled["droppederrors"] != codestyle.ModeError {
			t.Errorf("expected droppederrors to be ModeError, got %v", at.Enabled["droppederrors"])
		}
	})

	t.Run("mode_ignore", func(t *testing.T) {
		dir, r := setupModule(t, "lint:\n  droppedErrors:\n    mode: ignore\n")
		taskGroup, err := LintTasks(r, dir)
		if err != nil {
			t.Fatal(err)
		}
		at := findAnalyzersTask(taskGroup)
		if at != nil {
			if _, ok := at.Enabled["droppederrors"]; ok {
				t.Fatalf("expected droppederrors to be excluded in mode ignore")
			}
		}
	})
}

func TestLintTasks_SkipGenerated(t *testing.T) {
	setupModule := func(t *testing.T, apYAML string) (string, *sourcetree.SourceTree) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.27\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644); err != nil {
			t.Fatal(err)
		}
		var skips []string
		if apYAML != "" {
			apDir := filepath.Join(dir, ".ap")
			if err := os.MkdirAll(apDir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(apYAML), 0644); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			skips = cfg.Skip
		}
		r, err := sourcetree.Open(dir, sourcetree.WithSkip(skips...))
		if err != nil {
			t.Fatal(err)
		}
		return dir, r
	}

	t.Run("skipGenerated_true", func(t *testing.T) {
		dir, r := setupModule(t, "lint:\n  skipGenerated: true\n")
		taskGroup, err := LintTasks(r, dir)
		if err != nil {
			t.Fatal(err)
		}
		at := findAnalyzersTask(taskGroup)
		if at == nil {
			t.Fatalf("expected AnalyzersTask to be found")
		}
		if !at.SkipGenerated("droppederrors") {
			t.Errorf("expected droppederrors SkipGenerated to be true")
		}
		if !at.SkipGenerated("testcontext") {
			t.Errorf("expected testcontext SkipGenerated to be true")
		}
	})

	t.Run("default_skipGenerated", func(t *testing.T) {
		dir, r := setupModule(t, "")
		taskGroup, err := LintTasks(r, dir)
		if err != nil {
			t.Fatal(err)
		}
		at := findAnalyzersTask(taskGroup)
		if at == nil {
			t.Fatalf("expected AnalyzersTask to be found")
		}
		if at.SkipGenerated("droppederrors") {
			t.Errorf("expected droppederrors SkipGenerated to be false by default")
		}
		if at.SkipGenerated("testcontext") {
			t.Errorf("expected testcontext SkipGenerated to be false by default")
		}
	})
}

func findAnalyzersTask(taskGroup tasks.Task) *AnalyzersTask {
	group, ok := taskGroup.(*tasks.Group)
	if !ok {
		return nil
	}
	for _, t := range group.Tasks {
		if modGroup, ok := t.(*tasks.Group); ok {
			for _, sub := range modGroup.Tasks {
				if at, ok := sub.(*AnalyzersTask); ok {
					return at
				}
			}
		}
	}
	return nil
}

func TestLintTasks_PerCheckOverrides(t *testing.T) {
	setupModule := func(t *testing.T, apYAML string) (string, *sourcetree.SourceTree) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.27\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644); err != nil {
			t.Fatal(err)
		}
		var skips []string
		if apYAML != "" {
			apDir := filepath.Join(dir, ".ap")
			if err := os.MkdirAll(apDir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(apYAML), 0644); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			skips = cfg.Skip
		}
		r, err := sourcetree.Open(dir, sourcetree.WithSkip(skips...))
		if err != nil {
			t.Fatal(err)
		}
		return dir, r
	}

	t.Run("unused_skipGenerated_false_takes_effect", func(t *testing.T) {
		dir, r := setupModule(t, `
lint:
  unused:
    skipGenerated: false
    skipTests: true
`)
		taskGroup, err := LintTasks(r, dir)
		if err != nil {
			t.Fatal(err)
		}
		at := findAnalyzersTask(taskGroup)
		if at == nil {
			t.Fatalf("expected AnalyzersTask to be found")
		}
		if at.SkipGenerated("unused") {
			t.Errorf("expected unused SkipGenerated to be false when configured")
		}
		if !at.SkipTests("unused") {
			t.Errorf("expected unused SkipTests to be true when configured")
		}
	})

	t.Run("testcontext_and_any_overrides_take_effect", func(t *testing.T) {
		dir, r := setupModule(t, `
lint:
  testcontext:
    skipGenerated: true
    skipTests: true
  replaceEmptyInterfaceWithAny:
    skipGenerated: false
    skipTests: true
`)
		taskGroup, err := LintTasks(r, dir)
		if err != nil {
			t.Fatal(err)
		}
		at := findAnalyzersTask(taskGroup)
		if at == nil {
			t.Fatalf("expected AnalyzersTask to be found")
		}
		if !at.SkipGenerated("testcontext") {
			t.Errorf("expected testcontext SkipGenerated to be true")
		}
		if !at.SkipTests("testcontext") {
			t.Errorf("expected testcontext SkipTests to be true")
		}
		if at.SkipGenerated("replaceEmptyInterfaceWithAny") {
			t.Errorf("expected replaceEmptyInterfaceWithAny SkipGenerated to be false")
		}
		if !at.SkipTests("replaceEmptyInterfaceWithAny") {
			t.Errorf("expected replaceEmptyInterfaceWithAny SkipTests to be true")
		}
	})

	t.Run("unusedparameters_mode_error_enables_parameter_checking", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testmod\n\ngo 1.27\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc Foo(unusedParam int) {}\n"), 0644); err != nil {
			t.Fatal(err)
		}
		apDir := filepath.Join(dir, ".ap")
		if err := os.Mkdir(apDir, 0755); err != nil {
			t.Fatal(err)
		}
		yamlContent := `
lint:
  unusedparameters:
    mode: error
`
		if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(yamlContent), 0644); err != nil {
			t.Fatal(err)
		}
		r, err := sourcetree.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		taskGroup, err := LintTasks(r, dir)
		if err != nil {
			t.Fatal(err)
		}
		at := findAnalyzersTask(taskGroup)
		if at == nil {
			t.Fatalf("expected AnalyzersTask to be found")
		}
		err = at.Run(t.Context(), tasks.NewScope(dir, dir, r))
		if err == nil {
			t.Fatalf("expected unused parameter violation, got nil")
		}
		if !strings.Contains(err.Error(), "unused check failed") {
			t.Errorf("expected error to mention unused check failed, got %v", err)
		}
	})

	t.Run("unusedparameters_default_ignores_parameter_checking", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testmod\n\ngo 1.27\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc Foo(unusedParam int) {}\n"), 0644); err != nil {
			t.Fatal(err)
		}
		r, err := sourcetree.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		taskGroup, err := LintTasks(r, dir)
		if err != nil {
			t.Fatal(err)
		}
		at := findAnalyzersTask(taskGroup)
		if at == nil {
			t.Fatalf("expected AnalyzersTask to be found")
		}
		err = at.Run(t.Context(), tasks.NewScope(dir, dir, r))
		if err != nil {
			t.Fatalf("expected no violation when unusedparameters is not enabled, got %v", err)
		}
	})
}

func TestLintTasks_TestOnlyImports(t *testing.T) {
	setupModule := func(t *testing.T, apYAML string) (string, *sourcetree.SourceTree) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.27\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644); err != nil {
			t.Fatal(err)
		}
		var skips []string
		if apYAML != "" {
			apDir := filepath.Join(dir, ".ap")
			if err := os.MkdirAll(apDir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(apYAML), 0644); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			skips = cfg.Skip
		}
		r, err := sourcetree.Open(dir, sourcetree.WithSkip(skips...))
		if err != nil {
			t.Fatal(err)
		}
		return dir, r
	}

	t.Run("default_enabled_with_error_mode", func(t *testing.T) {
		dir, r := setupModule(t, "")
		taskGroup, err := LintTasks(r, dir)
		if err != nil {
			t.Fatal(err)
		}
		task := findAnalyzersTask(taskGroup)
		if task == nil {
			t.Fatalf("expected AnalyzersTask to be created by default")
		}
		if task.Enabled["testonlyimports"] != codestyle.ModeError {
			t.Errorf("expected ModeError by default, got %v", task.Enabled["testonlyimports"])
		}
		pkgs := task.Packages("testonlyimports")
		if len(pkgs) != 0 {
			t.Errorf("expected empty packages by default (analyzer defaults used), got %d", len(pkgs))
		}
		if task.SkipGenerated("testonlyimports") {
			t.Errorf("expected SkipGenerated to be false by default")
		}
		if task.SkipTests("testonlyimports") {
			t.Errorf("expected SkipTests to be false by default")
		}
	})

	t.Run("configured_warn_mode_and_custom_packages", func(t *testing.T) {
		dir, r := setupModule(t, `
lint:
  testOnlyImports:
    mode: warn
    packages:
      - github.com/google/go-cmp
    skipGenerated: true
    skipTests: true
`)
		taskGroup, err := LintTasks(r, dir)
		if err != nil {
			t.Fatal(err)
		}
		task := findAnalyzersTask(taskGroup)
		if task == nil {
			t.Fatalf("expected AnalyzersTask to be created")
		}
		if task.Enabled["testonlyimports"] != codestyle.ModeWarn {
			t.Errorf("expected ModeWarn in warn mode, got %v", task.Enabled["testonlyimports"])
		}
		pkgs := task.Packages("testonlyimports")
		if len(pkgs) != 1 || pkgs[0] != "github.com/google/go-cmp" {
			t.Errorf("unexpected packages: %v", pkgs)
		}
		if !task.SkipGenerated("testonlyimports") {
			t.Errorf("expected SkipGenerated to be true")
		}
		if !task.SkipTests("testonlyimports") {
			t.Errorf("expected SkipTests to be true")
		}
	})

	t.Run("mode_ignore_disables_task", func(t *testing.T) {
		dir, r := setupModule(t, `
lint:
  testOnlyImports:
    mode: ignore
`)
		taskGroup, err := LintTasks(r, dir)
		if err != nil {
			t.Fatal(err)
		}
		task := findAnalyzersTask(taskGroup)
		if task == nil {
			t.Fatalf("expected AnalyzersTask to be created for other analyzers")
		}
		if _, ok := task.Enabled["testonlyimports"]; ok {
			t.Fatalf("expected testonlyimports to not be enabled when mode is ignore")
		}
	})
}

func TestLintTasks_CloseWriter(t *testing.T) {
	setupModule := func(t *testing.T, yamlContent string) (string, *sourcetree.SourceTree) {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.27\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
			t.Fatal(err)
		}
		var skips []string
		if yamlContent != "" {
			apDir := filepath.Join(dir, ".ap")
			if err := os.Mkdir(apDir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(yamlContent), 0644); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			skips = cfg.Skip
		}
		r, err := sourcetree.Open(dir, sourcetree.WithSkip(skips...))
		if err != nil {
			t.Fatal(err)
		}
		return dir, r
	}

	t.Run("default_enabled_with_error_mode", func(t *testing.T) {
		dir, r := setupModule(t, "")
		taskGroup, err := LintTasks(r, dir)
		if err != nil {
			t.Fatal(err)
		}
		task := findAnalyzersTask(taskGroup)
		if task == nil {
			t.Fatalf("expected AnalyzersTask to be created by default")
		}
		if task.Enabled["closewriter"] != codestyle.ModeError {
			t.Errorf("expected ModeError by default, got %v", task.Enabled["closewriter"])
		}
		types := task.Types("closewriter")
		if len(types) != 0 {
			t.Errorf("expected empty types by default (analyzer defaults used), got %d", len(types))
		}
		if task.SkipGenerated("closewriter") {
			t.Errorf("expected SkipGenerated to be false by default")
		}
		if task.SkipTests("closewriter") {
			t.Errorf("expected SkipTests to be false by default")
		}
	})

	t.Run("configured_types_and_warn_mode", func(t *testing.T) {
		dir, r := setupModule(t, `
lint:
  closeWriter:
    mode: warn
    types:
      - "*bufio.Writer"
    skipGenerated: true
    skipTests: true
`)
		taskGroup, err := LintTasks(r, dir)
		if err != nil {
			t.Fatal(err)
		}
		task := findAnalyzersTask(taskGroup)
		if task == nil {
			t.Fatalf("expected AnalyzersTask to be created")
		}
		if task.Enabled["closewriter"] != codestyle.ModeWarn {
			t.Errorf("expected ModeWarn in warn mode, got %v", task.Enabled["closewriter"])
		}
		types := task.Types("closewriter")
		if len(types) != 1 || types[0] != "*bufio.Writer" {
			t.Errorf("unexpected types: %v", types)
		}
		if !task.SkipGenerated("closewriter") {
			t.Errorf("expected SkipGenerated to be true")
		}
		if !task.SkipTests("closewriter") {
			t.Errorf("expected SkipTests to be true")
		}
	})

	t.Run("mode_ignore_disables_task", func(t *testing.T) {
		dir, r := setupModule(t, `
lint:
  closeWriter:
    mode: ignore
`)
		taskGroup, err := LintTasks(r, dir)
		if err != nil {
			t.Fatal(err)
		}
		task := findAnalyzersTask(taskGroup)
		if task == nil {
			t.Fatalf("expected AnalyzersTask to be created for other analyzers")
		}
		if _, ok := task.Enabled["closewriter"]; ok {
			t.Fatalf("expected closewriter to not be enabled when mode is ignore")
		}
	})
}

func TestLintTasks_ContinueOnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.27\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := sourcetree.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	taskGroup, err := LintTasks(r, dir)
	if err != nil {
		t.Fatal(err)
	}

	outerGroup, ok := taskGroup.(*tasks.Group)
	if !ok {
		t.Fatalf("expected outer group to be *tasks.Group, got %T", taskGroup)
	}
	if !outerGroup.ContinueOnError {
		t.Errorf("expected outer go-lints group to have ContinueOnError: true")
	}

	if len(outerGroup.Tasks) != 1 {
		t.Fatalf("expected 1 module group, got %d", len(outerGroup.Tasks))
	}

	modGroup, ok := outerGroup.Tasks[0].(*tasks.Group)
	if !ok {
		t.Fatalf("expected module task to be *tasks.Group, got %T", outerGroup.Tasks[0])
	}
	if !modGroup.ContinueOnError {
		t.Errorf("expected module group %s to have ContinueOnError: true", modGroup.Name)
	}
}

func TestLintTasks_GovulncheckAndDroppedErrorsBothReport(t *testing.T) {
	errVuln := errors.New("govulncheck failed in /workspace/test: exit status 1")
	errDropped := errors.New("droppederrors check failed in /workspace/test: exit status 1")

	var ranVet, ranVuln, ranUnused, ranTC, ranAny, ranDropped bool

	modGroup := &tasks.Group{
		Name:            "go-lint-test",
		ContinueOnError: true,
		Tasks: []tasks.Task{
			&tasks.FuncTask{
				Name: "go-vet",
				RunFn: func(ctx context.Context, scope *tasks.APScope) error {
					ranVet = true
					return nil
				},
			},
			&tasks.FuncTask{
				Name: "govulncheck",
				RunFn: func(ctx context.Context, scope *tasks.APScope) error {
					ranVuln = true
					return errVuln
				},
			},
			&tasks.FuncTask{
				Name: "unused-check",
				RunFn: func(ctx context.Context, scope *tasks.APScope) error {
					ranUnused = true
					return nil
				},
			},
			&tasks.FuncTask{
				Name: "testcontext-check",
				RunFn: func(ctx context.Context, scope *tasks.APScope) error {
					ranTC = true
					return nil
				},
			},
			&tasks.FuncTask{
				Name: "replace-empty-interface-with-any-check",
				RunFn: func(ctx context.Context, scope *tasks.APScope) error {
					ranAny = true
					return nil
				},
			},
			&tasks.FuncTask{
				Name: "droppederrors-check",
				RunFn: func(ctx context.Context, scope *tasks.APScope) error {
					ranDropped = true
					return errDropped
				},
			},
		},
	}

	outerGroup := &tasks.Group{
		Name:            "go-lints",
		ContinueOnError: true,
		Tasks:           []tasks.Task{modGroup},
	}

	ctx := t.Context()
	tmpDir := t.TempDir()
	tree, err := sourcetree.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	scope := tasks.NewScope(tmpDir, tmpDir, tree)
	err = outerGroup.Run(ctx, scope)
	if err == nil {
		t.Fatalf("expected error from outerGroup.Run(), got nil")
	}

	// Verify both failing checks ran and didn't hide each other
	if !ranVuln {
		t.Errorf("expected govulncheck to run")
	}
	if !ranDropped {
		t.Errorf("expected droppederrors to run despite govulncheck failing")
	}
	if !ranVet || !ranUnused || !ranTC || !ranAny {
		t.Errorf("expected all checks to run")
	}

	// Verify both errors are in the returned joined error
	if !errors.Is(err, errVuln) {
		t.Errorf("expected joined error to contain govulncheck error")
	}
	if !errors.Is(err, errDropped) {
		t.Errorf("expected joined error to contain droppederrors error")
	}
}

func TestLintTasks_PackageLoadError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.27\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Invalid Go file with syntax error that causes package load to fail.
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\ninvalid syntax here\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := sourcetree.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	taskGroup, err := LintTasks(r, dir)
	if err != nil {
		t.Fatal(err)
	}

	task := findAnalyzersTask(taskGroup)
	if task == nil {
		t.Fatalf("expected AnalyzersTask to be created")
	}

	ctx := t.Context()
	err = task.Run(ctx, tasks.NewScope(dir, dir, r))
	if err == nil {
		t.Fatalf("expected task.Run to fail on package load error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to load packages") {
		t.Errorf("expected error to mention failed to load packages, got: %v", err)
	}
}

func TestLintTasks_MultiGOOS_LinuxOnDarwin(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.27\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// _linux.go file containing an unused function
	if err := os.WriteFile(filepath.Join(dir, "foo_linux.go"), []byte("package main\n\nfunc unusedInLinux() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	r, err := sourcetree.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	taskGroup, err := LintTasks(r, dir)
	if err != nil {
		t.Fatal(err)
	}

	task := findAnalyzersTask(taskGroup)
	if task == nil {
		t.Fatalf("expected AnalyzersTask to be created")
	}

	// Simulate running on macOS (darwin host)
	task.HostGOOS = "darwin"
	task.GOOS = determineTargetGOOS(r, dir, nil, "darwin")

	if len(task.GOOS) != 2 || task.GOOS[0] != "darwin" || task.GOOS[1] != "linux" {
		t.Fatalf("expected target GOOS [darwin, linux], got %v", task.GOOS)
	}

	ctx := t.Context()
	err = task.Run(ctx, tasks.NewScope(dir, dir, r))
	if err == nil {
		t.Fatalf("expected unused error in foo_linux.go to be reported on simulated darwin, got nil")
	}
	if !strings.Contains(err.Error(), "unused check failed") {
		t.Errorf("expected error to mention unused check failed, got: %v", err)
	}
	if task.LoadCount != 2 {
		t.Errorf("expected 2 package loads (darwin + linux), got %d", task.LoadCount)
	}
}

func TestLintTasks_MultiGOOS_SingleLoad(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.27\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	r, err := sourcetree.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	taskGroup, err := LintTasks(r, dir)
	if err != nil {
		t.Fatal(err)
	}

	task := findAnalyzersTask(taskGroup)
	if task == nil {
		t.Fatalf("expected AnalyzersTask to be created")
	}

	ctx := t.Context()
	err = task.Run(ctx, tasks.NewScope(dir, dir, r))
	if err != nil {
		t.Fatalf("unexpected error from task.Run: %v", err)
	}

	// No OS-specific files -> exactly 1 package load
	if task.LoadCount != 1 {
		t.Errorf("expected exactly 1 package load, got %d", task.LoadCount)
	}
}

func TestLintTasks_MultiGOOS_VendorIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.27\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create vendor directory with _linux.go file
	vendorDir := filepath.Join(dir, "vendor", "example.com", "pkg")
	if err := os.MkdirAll(vendorDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vendorDir, "foo_linux.go"), []byte("package pkg\n\nfunc unusedInVendor() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	r, err := sourcetree.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate macOS host where vendor has _linux.go
	targets := determineTargetGOOS(r, dir, nil, "darwin")
	if len(targets) != 1 || targets[0] != "darwin" {
		t.Fatalf("expected vendor _linux.go to not trigger extra load; got targets: %v", targets)
	}

	taskGroup, err := LintTasks(r, dir)
	if err != nil {
		t.Fatal(err)
	}

	task := findAnalyzersTask(taskGroup)
	if task == nil {
		t.Fatalf("expected AnalyzersTask to be created")
	}

	ctx := t.Context()
	err = task.Run(ctx, tasks.NewScope(dir, dir, r))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if task.LoadCount != 1 {
		t.Errorf("expected exactly 1 load when OS file is under vendor/, got %d", task.LoadCount)
	}
}

func TestLintTasks_MultiGOOS_ExplicitOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.27\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	apDir := filepath.Join(dir, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	yaml := `
lint:
  goos:
    - linux
    - darwin
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	r, err := sourcetree.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	taskGroup, err := LintTasks(r, dir)
	if err != nil {
		t.Fatal(err)
	}

	task := findAnalyzersTask(taskGroup)
	if task == nil {
		t.Fatalf("expected AnalyzersTask to be created")
	}

	if len(task.GOOS) != 2 || task.GOOS[0] != "linux" || task.GOOS[1] != "darwin" {
		t.Fatalf("expected explicit GOOS override [linux, darwin], got %v", task.GOOS)
	}
}

func TestAnalyzersTask_DroppedErrorsBaselineRatchet(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.27\n"), 0644); err != nil {
		t.Fatal(err)
	}

	codeV1 := `package main

import "errors"

func f1() error { return errors.New("1") }
func f2() error { return errors.New("2") }

func main() {
	_ = f1()
	_ = f2()
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(codeV1), 0644); err != nil {
		t.Fatal(err)
	}

	baselinePath := filepath.Join(dir, ".ap", "droppederrors-baseline.txt")

	r, err := sourcetree.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Write baseline via generic task
	taskGroup, err := LintTasks(r, dir)
	if err != nil {
		t.Fatal(err)
	}
	task := findAnalyzersTask(taskGroup)
	if task == nil {
		t.Fatal("expected AnalyzersTask")
	}
	task.WriteBaseline = true
	task.BaselinePath = baselinePath

	ctx := t.Context()
	scope := tasks.NewScope(dir, dir, r)
	if err := task.Run(ctx, scope); err != nil {
		t.Fatalf("write baseline failed: %v", err)
	}

	if _, err := os.Stat(baselinePath); err != nil {
		t.Fatalf("expected baseline file to be created: %v", err)
	}

	// 2. Check with baseline in mode error -> should PASS
	taskGroup2, err := LintTasks(r, dir)
	if err != nil {
		t.Fatal(err)
	}
	task2 := findAnalyzersTask(taskGroup2)
	task2.BaselinePath = baselinePath
	if err := task2.Run(ctx, scope); err != nil {
		t.Fatalf("check with baseline failed unexpectedly: %v", err)
	}

	// 3. Add a new dropped error: f3() -> should FAIL
	codeV2 := `package main

import "errors"

func f1() error { return errors.New("1") }
func f2() error { return errors.New("2") }
func f3() error { return errors.New("3") }

func main() {
	_ = f1()
	_ = f2()
	_ = f3()
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(codeV2), 0644); err != nil {
		t.Fatal(err)
	}
	r, err = sourcetree.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	taskGroup3, err := LintTasks(r, dir)
	if err != nil {
		t.Fatal(err)
	}
	task3 := findAnalyzersTask(taskGroup3)
	task3.BaselinePath = baselinePath
	err = task3.Run(ctx, scope)
	if err == nil {
		t.Fatalf("expected new dropped error to fail check")
	}
	if !strings.Contains(err.Error(), "droppederrors check failed") {
		t.Errorf("expected droppederrors failure error, got: %v", err)
	}

	// 4. Fix f2() -> f1 is tolerated, f2 is stale
	codeV3 := `package main

import "errors"

func f1() error { return errors.New("1") }
func f2() error { return errors.New("2") }

func main() {
	_ = f1()
	if err := f2(); err != nil {
		// handled
	}
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(codeV3), 0644); err != nil {
		t.Fatal(err)
	}
	r, err = sourcetree.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	taskGroup4, err := LintTasks(r, dir)
	if err != nil {
		t.Fatal(err)
	}
	task4 := findAnalyzersTask(taskGroup4)
	task4.BaselinePath = baselinePath
	// Should pass because remaining finding f1 is in baseline
	if err := task4.Run(ctx, scope); err != nil {
		t.Fatalf("expected check to pass after fixing f2: %v", err)
	}
}

func TestLintTasks_SubtreeOverrides(t *testing.T) {
	repoRoot := t.TempDir()
	apDir := filepath.Join(repoRoot, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	baseYAML := `
govet:
  enabled: true
govulncheck:
  enabled: true
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(baseYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Root module
	if err := os.WriteFile(filepath.Join(repoRoot, "go.mod"), []byte("module rootmod\n\ngo 1.27\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Subtree module with govulncheck disabled in .ap/go.yaml and version: latest in .ap/ap.yaml
	subDir := filepath.Join(repoRoot, "sub")
	if err := os.MkdirAll(filepath.Join(subDir, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, ".ap", "ap.yaml"), []byte("version: latest\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, ".ap", "go.yaml"), []byte("govulncheck:\n  enabled: false\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "go.mod"), []byte("module submod\n\ngo 1.27\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "sub.go"), []byte("package sub\n\nfunc Sub() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	r, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	tasksGroup, err := LintTasks(r, repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	outer, ok := tasksGroup.(*tasks.Group)
	if !ok {
		t.Fatalf("expected outer group *tasks.Group, got %T", tasksGroup)
	}

	hasGovulncheck := func(g *tasks.Group) bool {
		for _, subTask := range g.Tasks {
			if _, ok := subTask.(*GovulncheckTask); ok {
				return true
			}
		}
		return false
	}

	var rootGroup, subGroup *tasks.Group
	for _, task := range outer.Tasks {
		g, ok := task.(*tasks.Group)
		if !ok {
			continue
		}
		if g.Name == "go-lint-root" || g.Name == fmt.Sprintf("go-lint-%s", filepath.Base(repoRoot)) {
			rootGroup = g
		} else if g.Name == "go-lint-sub" {
			subGroup = g
		}
	}

	if rootGroup == nil {
		t.Fatalf("expected root module group, got tasks: %+v", outer.Tasks)
	}
	if subGroup == nil {
		t.Fatalf("expected sub module group, got tasks: %+v", outer.Tasks)
	}

	if !hasGovulncheck(rootGroup) {
		t.Errorf("expected root module to have govulncheck enabled")
	}
	if hasGovulncheck(subGroup) {
		t.Errorf("expected sub module to have govulncheck disabled via subtree override")
	}
}
