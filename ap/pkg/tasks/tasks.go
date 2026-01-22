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

// Task represents a discoverable task script.
type Task struct {
	Name string
	Path string
}

// Find looks for executable scripts in dev/tasks that match the prefix.
// It can optionally exclude scripts matching an excludePrefix.
func Find(root string, prefix string, excludePrefix string) ([]Task, error) {
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
		if strings.HasPrefix(name, prefix) {
			if excludePrefix != "" && strings.HasPrefix(name, excludePrefix) {
				continue
			}
			tasks = append(tasks, Task{
				Name: name,
				Path: filepath.Join(tasksDir, name),
			})
		}
	}

	// Sort by name for deterministic order
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].Name < tasks[j].Name
	})

	return tasks, nil
}

// Run executes a list of tasks.
func Run(ctx context.Context, root string, tasks []Task) error {
	for _, task := range tasks {
		klog.Infof("Running task: %s", task.Name)
		cmd := exec.CommandContext(ctx, task.Path)
		cmd.Dir = root
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("task %s failed: %w", task.Name, err)
		}
	}
	return nil
}
