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
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"k8s.io/klog/v2"
)

// Task is the interface that all tasks must implement.
type Task interface {
	Run(ctx context.Context, root string) error
	GetName() string
	GetChildren() []Task
}

// TaskScript represents a discoverable task script.
type TaskScript struct {
	Name string
	Path string
}

func (t *TaskScript) Run(ctx context.Context, root string) error {
	klog.Infof("Running task: %s", t.Name)
	cmd := exec.CommandContext(ctx, t.Path)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
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

// Group represents a collection of tasks.
type Group struct {
	Name  string
	Tasks []Task
}

func (g *Group) Run(ctx context.Context, root string) error {
	for _, t := range g.Tasks {
		if err := t.Run(ctx, root); err != nil {
			return err
		}
	}
	return nil
}

func (g *Group) GetName() string {
	return g.Name
}

func (g *Group) GetChildren() []Task {
	return g.Tasks
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

// FindTaskScripts looks for executable scripts in dev/tasks that match the prefix.
func FindTaskScripts(root string, opts ...FindOption) ([]Task, error) {
	options := FindOptions{}
	for _, o := range opts {
		o(&options)
	}

	tasksDir := filepath.Join(root, "dev", "tasks")
	entries, err := os.ReadDir(tasksDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read tasks dir: %w", err)
	}

	var tasks []Task
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if options.Prefix != "" && !strings.HasPrefix(name, options.Prefix) {
			continue
		}
		if options.ExcludePrefix != "" && strings.HasPrefix(name, options.ExcludePrefix) {
			continue
		}
		tasks = append(tasks, &TaskScript{
			Name: name,
			Path: filepath.Join(tasksDir, name),
		})
	}

	// Sort by name for deterministic order
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].GetName() < tasks[j].GetName()
	})

	return tasks, nil
}

// RunOptions holds options for running tasks.
type RunOptions struct {
	DryRun bool
}

// Run executes a list of tasks.
func Run(ctx context.Context, root string, tasks []Task, opts RunOptions) error {
	if opts.DryRun {
		for _, task := range tasks {
			PrintTree(task, 0)
		}
		return nil
	}
	for _, task := range tasks {
		if err := task.Run(ctx, root); err != nil {
			return err
		}
	}
	return nil
}

// PrintTree prints the task tree to stdout.
func PrintTree(t Task, indent int) {
	fmt.Printf("%s%s\n", strings.Repeat("  ", indent), t.GetName())
	for _, child := range t.GetChildren() {
		PrintTree(child, indent+1)
	}
}
