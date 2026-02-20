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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"github.com/gke-labs/gke-labs-infra/codestyle/pkg/walker"
	"k8s.io/klog/v2"
)

// GoVetTask represents a task to run go vet.
type GoVetTask struct {
	Dir string
}

func (t *GoVetTask) Run(ctx context.Context, root string) error {
	klog.Infof("Running go vet in %s", t.Dir)
	vetCmd := exec.CommandContext(ctx, "go", "vet", "./...")
	vetCmd.Dir = t.Dir
	vetCmd.Stdout = os.Stdout
	vetCmd.Stderr = os.Stderr
	if err := vetCmd.Run(); err != nil {
		return fmt.Errorf("go vet failed in %s: %w", t.Dir, err)
	}
	return nil
}

func (t *GoVetTask) GetName() string {
	return "go-vet"
}

func (t *GoVetTask) GetChildren() []tasks.Task {
	return nil
}

// GovulncheckTask represents a task to run govulncheck.
type GovulncheckTask struct {
	Dir string
}

func (t *GovulncheckTask) Run(ctx context.Context, root string) error {
	klog.Infof("Running govulncheck in %s", t.Dir)
	vulnCmd := exec.CommandContext(ctx, "go", "run", "golang.org/x/vuln/cmd/govulncheck@latest", "./...")
	vulnCmd.Dir = t.Dir
	vulnCmd.Stdout = os.Stdout
	vulnCmd.Stderr = os.Stderr
	if err := vulnCmd.Run(); err != nil {
		return fmt.Errorf("govulncheck failed in %s: %w", t.Dir, err)
	}
	return nil
}

func (t *GovulncheckTask) GetName() string {
	return "govulncheck"
}

func (t *GovulncheckTask) GetChildren() []tasks.Task {
	return nil
}

// UnusedCheckTask represents a task to run unused check.
type UnusedCheckTask struct {
	Dir             string
	CheckParameters bool
}

func (t *UnusedCheckTask) Run(ctx context.Context, root string) error {
	klog.Infof("Running unused check in %s", t.Dir)
	apPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not find ap executable: %w", err)
	}
	args := []string{"lint", "unused"}
	if t.CheckParameters {
		args = append(args, "-unused.check-parameters=true")
	} else {
		args = append(args, "-unused.check-parameters=false")
	}
	args = append(args, "./...")
	unusedCmd := exec.CommandContext(ctx, apPath, args...)
	unusedCmd.Dir = t.Dir
	unusedCmd.Stdout = os.Stdout
	unusedCmd.Stderr = os.Stderr
	if err := unusedCmd.Run(); err != nil {
		return fmt.Errorf("unused check failed in %s: %w", t.Dir, err)
	}
	return nil
}

func (t *UnusedCheckTask) GetName() string {
	return "unused-check"
}

func (t *UnusedCheckTask) GetChildren() []tasks.Task {
	return nil
}

// TestContextCheckTask represents a task to run testcontext check.
type TestContextCheckTask struct {
	Dir      string
	IsError  bool
}

func (t *TestContextCheckTask) Run(ctx context.Context, root string) error {
	klog.Infof("Running testcontext check in %s", t.Dir)
	apPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not find ap executable: %w", err)
	}
	args := []string{"lint", "testcontext", "./..."}
	testcontextCmd := exec.CommandContext(ctx, apPath, args...)
	testcontextCmd.Dir = t.Dir
	testcontextCmd.Stdout = os.Stdout
	testcontextCmd.Stderr = os.Stderr
	if err := testcontextCmd.Run(); err != nil {
		if t.IsError {
			return fmt.Errorf("testcontext check failed in %s: %w", t.Dir, err)
		}
		klog.Warningf("testcontext check failed in %s: %v", t.Dir, err)
	}
	return nil
}

func (t *TestContextCheckTask) GetName() string {
	return "testcontext-check"
}

func (t *TestContextCheckTask) GetChildren() []tasks.Task {
	return nil
}

// LintTasks returns a task group for running go linting in discovered modules.
func LintTasks(root string) (tasks.Task, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}

	// Find all go.mod files
	ignoreList := walker.NewIgnoreList([]string{".git", "vendor", "node_modules"})
	goMods, err := walker.Walk(root, ignoreList, func(_ string, info os.FileInfo) bool {
		return info.Name() == "go.mod"
	})
	if err != nil {
		return nil, err
	}

	var moduleTasks []tasks.Task
	for _, goMod := range goMods {
		dir := filepath.Dir(goMod)

		hasGo, err := hasGoFiles(dir)
		if err != nil {
			return nil, fmt.Errorf("failed to check for Go files in %s: %w", dir, err)
		}
		if !hasGo {
			continue
		}

		modGroup := &tasks.Group{
			Name: fmt.Sprintf("go-lint-%s", filepath.Base(dir)),
		}

		if cfg.IsGovetEnabled() {
			modGroup.Tasks = append(modGroup.Tasks, &GoVetTask{Dir: dir})
		}
		if cfg.IsGovulncheckEnabled() {
			modGroup.Tasks = append(modGroup.Tasks, &GovulncheckTask{Dir: dir})
		}
		if cfg.IsUnusedEnabled() {
			modGroup.Tasks = append(modGroup.Tasks, &UnusedCheckTask{
				Dir:             dir,
				CheckParameters: cfg.IsUnusedParametersEnabled(),
			})
		}
		if cfg.IsTestContextEnabled() {
			modGroup.Tasks = append(modGroup.Tasks, &TestContextCheckTask{
				Dir:     dir,
				IsError: cfg.IsTestContextError(),
			})
		}

		if len(modGroup.Tasks) > 0 {
			moduleTasks = append(moduleTasks, modGroup)
		}
	}

	return &tasks.Group{
		Name:  "go-lints",
		Tasks: moduleTasks,
	}, nil
}

// Lint runs go vet and govulncheck in discovered modules.
func Lint(ctx context.Context, root string) error {
	t, err := LintTasks(root)
	if err != nil {
		return err
	}
	return t.Run(ctx, root)
}

// hasGoFiles returns true if the directory or any of its subdirectories
// (excluding those that are themselves Go modules) contain at least one .go file.
func hasGoFiles(root string) (bool, error) {
	found := false
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root {
				// If this directory contains a go.mod file, it's a separate module.
				// We should not look for Go files inside it.
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if filepath.Ext(path) == ".go" {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	if err == filepath.SkipAll {
		err = nil
	}
	return found, err
}
