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

package tasks

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"k8s.io/klog/v2"
)

// TaskScript represents a discoverable task script.
type TaskScript struct {
	Name string
	Path string
	Root string
}

func (t *TaskScript) Run(ctx context.Context, scope *APScope) error {
	klog.Infof("Running task: %s", t.Name)
	cmd := exec.CommandContext(ctx, t.Path)
	cmd.Dir = t.Root
	env, err := scope.ScriptEnv()
	if err != nil {
		return fmt.Errorf("failed to prepare script environment for %s: %w", t.Name, err)
	}
	cmd.Env = env
	cmd.Stdout = scope.GetStdout()
	cmd.Stderr = scope.GetStderr()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("task %s failed: %w", t.Name, err)
	}
	return nil
}

func (t *TaskScript) GetName() string {
	return t.Name
}

func (t *TaskScript) GetChildren() []Task {
	return nil
}

type FindOptions struct {
	Prefix        string
	ExcludePrefix string
}

type FindOption func(*FindOptions)

func WithPrefix(prefix string) FindOption {
	return func(o *FindOptions) {
		o.Prefix = prefix
	}
}

func WithExcludePrefix(prefix string) FindOption {
	return func(o *FindOptions) {
		o.ExcludePrefix = prefix
	}
}

// FindTaskScripts looks for executable scripts in dev/tasks anywhere under root that match the prefix.
func FindTaskScripts(r *sourcetree.SourceTree, root string, opts ...FindOption) ([]Task, error) {
	if r == nil {
		return nil, fmt.Errorf("sourcetree is nil")
	}
	options := FindOptions{}
	for _, o := range opts {
		o(&options)
	}

	var tasks []Task
	for _, f := range r.Under(root).Files() {
		dir := filepath.Dir(f.Path)
		if filepath.Base(dir) != "tasks" {
			continue
		}
		parentDev := filepath.Dir(dir)
		if filepath.Base(parentDev) != "dev" {
			continue
		}

		name := f.Name
		if options.Prefix != "" && !strings.HasPrefix(name, options.Prefix) {
			continue
		}
		if options.ExcludePrefix != "" && strings.HasPrefix(name, options.ExcludePrefix) {
			continue
		}

		taskRoot := filepath.Dir(parentDev)
		tasks = append(tasks, &TaskScript{
			Name: name,
			Path: f.Path,
			Root: taskRoot,
		})
	}

	// Sort by name for deterministic order (tie-break by path)
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].GetName() == tasks[j].GetName() {
			return tasks[i].(*TaskScript).Path < tasks[j].(*TaskScript).Path
		}
		return tasks[i].GetName() < tasks[j].GetName()
	})

	return tasks, nil
}
