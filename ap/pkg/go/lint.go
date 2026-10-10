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

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/walker"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"k8s.io/klog/v2"
)

// GoVetTask represents a task to run go vet.
type GoVetTask struct {
	Dir string
}

func (t *GoVetTask) Run(ctx context.Context, scope *tasks.APScope) error {
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

func (t *GovulncheckTask) Run(ctx context.Context, scope *tasks.APScope) error {
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
	RepoRoot        string
	CheckParameters bool
	SkipGenerated   bool
	SkipTests       bool
	SkipGlobs       []string
}

func (t *UnusedCheckTask) Run(ctx context.Context, scope *tasks.APScope) error {
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
	args = append(args, fmt.Sprintf("-unused.skip-generated=%t", t.SkipGenerated))
	args = append(args, fmt.Sprintf("-unused.skip-tests=%t", t.SkipTests))
	if t.RepoRoot != "" {
		args = append(args, "-unused.repo-root="+t.RepoRoot)
	}
	for _, g := range t.SkipGlobs {
		args = append(args, "-unused.skip-glob="+g)
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
	Dir           string
	RepoRoot      string
	IsError       bool
	SkipGenerated bool
	SkipTests     bool
	SkipGlobs     []string
}

func (t *TestContextCheckTask) Run(ctx context.Context, scope *tasks.APScope) error {
	klog.Infof("Running testcontext check in %s", t.Dir)
	apPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not find ap executable: %w", err)
	}
	args := []string{"lint", "testcontext"}
	args = append(args, fmt.Sprintf("-testcontext.skip-generated=%t", t.SkipGenerated))
	args = append(args, fmt.Sprintf("-testcontext.skip-tests=%t", t.SkipTests))
	if t.RepoRoot != "" {
		args = append(args, "-testcontext.repo-root="+t.RepoRoot)
	}
	for _, g := range t.SkipGlobs {
		args = append(args, "-testcontext.skip-glob="+g)
	}
	args = append(args, "./...")
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

// ReplaceEmptyInterfaceWithAnyTask represents a task to run replace-empty-interface-with-any check.
type ReplaceEmptyInterfaceWithAnyTask struct {
	Dir           string
	RepoRoot      string
	SkipGenerated bool
	SkipTests     bool
	SkipGlobs     []string
}

func (t *ReplaceEmptyInterfaceWithAnyTask) Run(ctx context.Context, scope *tasks.APScope) error {
	klog.Infof("Running replace-empty-interface-with-any check in %s", t.Dir)
	apPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not find ap executable: %w", err)
	}
	args := []string{"lint", "replace-empty-interface-with-any"}
	args = append(args, fmt.Sprintf("-replaceEmptyInterfaceWithAny.skip-generated=%t", t.SkipGenerated))
	args = append(args, fmt.Sprintf("-replaceEmptyInterfaceWithAny.skip-tests=%t", t.SkipTests))
	if t.RepoRoot != "" {
		args = append(args, "-replaceEmptyInterfaceWithAny.repo-root="+t.RepoRoot)
	}
	for _, g := range t.SkipGlobs {
		args = append(args, "-replaceEmptyInterfaceWithAny.skip-glob="+g)
	}
	args = append(args, "./...")
	anyCmd := exec.CommandContext(ctx, apPath, args...)
	anyCmd.Dir = t.Dir
	anyCmd.Stdout = os.Stdout
	anyCmd.Stderr = os.Stderr
	if err := anyCmd.Run(); err != nil {
		klog.Warningf("replace-empty-interface-with-any check failed in %s: %v", t.Dir, err)
	}
	return nil
}

func (t *ReplaceEmptyInterfaceWithAnyTask) GetName() string {
	return "replace-empty-interface-with-any-check"
}

func (t *ReplaceEmptyInterfaceWithAnyTask) GetChildren() []tasks.Task {
	return nil
}

// DroppedErrorsCheckTask represents a task to run droppederrors check.
type DroppedErrorsCheckTask struct {
	Dir           string
	RepoRoot      string
	IsError       bool
	SkipGenerated bool
	SkipTests     bool
	SkipGlobs     []string
}

func (t *DroppedErrorsCheckTask) Run(ctx context.Context, scope *tasks.APScope) error {
	klog.Infof("Running droppederrors check in %s", t.Dir)
	apPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not find ap executable: %w", err)
	}
	args := []string{"lint", "droppederrors"}
	args = append(args, fmt.Sprintf("--skip-generated=%t", t.SkipGenerated))
	args = append(args, fmt.Sprintf("--skip-tests=%t", t.SkipTests))
	args = append(args, "./...")
	droppedCmd := exec.CommandContext(ctx, apPath, args...)
	droppedCmd.Dir = t.Dir
	droppedCmd.Stdout = os.Stdout
	droppedCmd.Stderr = os.Stderr
	if err := droppedCmd.Run(); err != nil {
		if t.IsError {
			return fmt.Errorf("droppederrors check failed in %s: %w", t.Dir, err)
		}
		klog.Warningf("droppederrors check failed in %s: %v", t.Dir, err)
	}
	return nil
}

func (t *DroppedErrorsCheckTask) GetName() string {
	return "droppederrors-check"
}

func (t *DroppedErrorsCheckTask) GetChildren() []tasks.Task {
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
				RepoRoot:        root,
				CheckParameters: cfg.IsUnusedParametersEnabled(),
				SkipGenerated:   cfg.ResolveSkipGenerated(nil, true),
				SkipTests:       cfg.ResolveSkipTests(nil, false),
				SkipGlobs:       cfg.Skip,
			})
		}
		if cfg.IsTestContextEnabled() {
			modGroup.Tasks = append(modGroup.Tasks, &TestContextCheckTask{
				Dir:           dir,
				RepoRoot:      root,
				IsError:       cfg.IsTestContextError(),
				SkipGenerated: cfg.ResolveSkipGenerated(nil, false),
				SkipTests:     cfg.ResolveSkipTests(nil, false),
				SkipGlobs:     cfg.Skip,
			})
		}
		if cfg.IsReplaceEmptyInterfaceWithAnyEnabled() {
			modGroup.Tasks = append(modGroup.Tasks, &ReplaceEmptyInterfaceWithAnyTask{
				Dir:           dir,
				RepoRoot:      root,
				SkipGenerated: cfg.ResolveSkipGenerated(nil, true),
				SkipTests:     cfg.ResolveSkipTests(nil, false),
				SkipGlobs:     cfg.Skip,
			})
		}
		if cfg.IsDroppedErrorsEnabled() {
			modGroup.Tasks = append(modGroup.Tasks, &DroppedErrorsCheckTask{
				Dir:           dir,
				RepoRoot:      root,
				IsError:       cfg.IsDroppedErrorsError(),
				SkipGenerated: cfg.DroppedErrorsSkipGenerated(),
				SkipTests:     cfg.DroppedErrorsSkipTests(),
				SkipGlobs:     cfg.Skip,
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
	return t.Run(ctx, &tasks.APScope{RepoRoot: root, Dir: root})
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
				hasGoMod, err := fileutils.FileExists(filepath.Join(path, "go.mod"))
				if err != nil {
					return err
				}
				if hasGoMod {
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
