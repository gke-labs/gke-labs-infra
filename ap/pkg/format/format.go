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

package format

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/fileheaders"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/gostyle"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"k8s.io/klog/v2"
)

// CodestyleTask represents a task to run codestyle checks (headers, gofmt, etc).
type CodestyleTask struct {
	Root  string
	Files []string
}

func (t *CodestyleTask) Run(ctx context.Context, scope *tasks.TaskContext) error {
	klog.Infof("Running codestyle in %s...", t.Root)
	r := scope.SourceTree()
	if err := fileheaders.Run(ctx, r, t.Root, t.Files); err != nil {
		return fmt.Errorf("fileheaders failed in %s: %w", t.Root, err)
	}
	if err := gostyle.Run(ctx, r, t.Root, t.Files, scope.Stdout(), scope.Stderr()); err != nil {
		return fmt.Errorf("gostyle failed in %s: %w", t.Root, err)
	}
	return nil
}

func (t *CodestyleTask) GetName() string {
	return "codestyle"
}

func (t *CodestyleTask) GetChildren() []tasks.Task {
	return nil
}

// LegacyFormatScriptTask represents a task to run a legacy format script.
type LegacyFormatScriptTask struct {
	Name string
	Path string
}

func (t *LegacyFormatScriptTask) Run(ctx context.Context, scope *tasks.TaskContext) error {
	klog.Infof("Running legacy format script: %s", t.Name)
	cmd := exec.CommandContext(ctx, t.Path)
	cmd.Dir = scope.Dir()
	env, err := scope.ScriptEnv()
	if err != nil {
		return fmt.Errorf("failed to prepare script environment for %s: %w", t.Name, err)
	}
	cmd.Env = env
	cmd.Stdout = scope.Stdout()
	cmd.Stderr = scope.Stderr()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run %s: %w", t.Name, err)
	}
	return nil
}

func (t *LegacyFormatScriptTask) GetName() string {
	return fmt.Sprintf("legacy-format-%s", t.Name)
}

func (t *LegacyFormatScriptTask) GetChildren() []tasks.Task {
	return nil
}

// FormatTasks returns a task group for all formatting tasks.
func FormatTasks(r *sourcetree.SourceTree, root string, files ...string) (tasks.Task, error) {
	if r == nil {
		return nil, fmt.Errorf("sourcetree is nil")
	}
	var allTasks []tasks.Task

	// 1. Run codestyle (headers, gofmt, etc)
	allTasks = append(allTasks, &CodestyleTask{Root: root, Files: files})

	// 2. Run legacy format scripts
	tasksDir := filepath.Join(root, "dev", "tasks")
	for _, f := range r.FilesInDir(tasksDir) {
		name := f.Name
		if strings.HasPrefix(name, "format-") {
			allTasks = append(allTasks, &LegacyFormatScriptTask{
				Name: strings.TrimPrefix(name, "format-"),
				Path: f.Path,
			})
		}
	}

	return &tasks.Group{
		Name:            "format",
		Tasks:           allTasks,
		ContinueOnError: true,
	}, nil
}

func Run(ctx context.Context, r *sourcetree.SourceTree, root string) error {
	t, err := FormatTasks(r, root)
	if err != nil {
		return err
	}
	return t.Run(ctx, tasks.NewTaskContext(root, root, r))
}
