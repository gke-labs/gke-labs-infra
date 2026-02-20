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
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"github.com/gke-labs/gke-labs-infra/codestyle/pkg/fileheaders"
	"github.com/gke-labs/gke-labs-infra/codestyle/pkg/gostyle"
	"k8s.io/klog/v2"
)

// CodestyleTask represents a task to run codestyle checks (headers, gofmt, etc).
type CodestyleTask struct {
}

func (t *CodestyleTask) Run(ctx context.Context, root string) error {
	klog.Info("Running codestyle...")
	if err := fileheaders.Run(ctx, root, nil); err != nil {
		return fmt.Errorf("fileheaders failed: %w", err)
	}
	if err := gostyle.Run(ctx, root, nil); err != nil {
		return fmt.Errorf("gostyle failed: %w", err)
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

func (t *LegacyFormatScriptTask) Run(ctx context.Context, root string) error {
	klog.Infof("Running legacy format script: %s", t.Name)
	cmd := exec.CommandContext(ctx, t.Path)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
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
func FormatTasks(root string) (tasks.Task, error) {
	var allTasks []tasks.Task

	// 1. Run codestyle (headers, gofmt, etc)
	allTasks = append(allTasks, &CodestyleTask{})

	// 2. Run legacy format scripts
	tasksDir := filepath.Join(root, "dev", "tasks")
	entries, err := os.ReadDir(tasksDir)
	if err == nil {
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, "format-") && !entry.IsDir() {
				allTasks = append(allTasks, &LegacyFormatScriptTask{
					Name: name,
					Path: filepath.Join(tasksDir, name),
				})
			}
		}
	}

	return &tasks.Group{
		Name:  "format",
		Tasks: allTasks,
	}, nil
}

func Run(ctx context.Context, root string) error {
	t, err := FormatTasks(root)
	if err != nil {
		return err
	}
	return t.Run(ctx, root)
}
