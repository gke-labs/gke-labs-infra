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
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/images"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"github.com/spf13/cobra"
)

// LsOptions holds the configuration for the "ls" command.
type LsOptions struct {
	*RootOptions
}

// BuildLsCommand constructs the cobra command for "ls".
func BuildLsCommand(rootOpt *RootOptions) *cobra.Command {
	opt := LsOptions{
		RootOptions: rootOpt,
	}

	cmd := &cobra.Command{
		Use:   "ls [path...]",
		Short: "List AP roots",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opt.Resolve(args); err != nil {
				return err
			}
			return RunLs(cmd.Context(), opt)
		},
	}

	return cmd
}

// RunLs executes the business logic for the "ls" command.
func RunLs(ctx context.Context, opt LsOptions) error {
	if err := requireRepoRoot(opt.RootOptions); err != nil {
		return err
	}

	r, err := opt.GetSourceTree()
	if err != nil {
		return err
	}

	buildkitHost, err := images.ResolveBuildkitHost("")
	if err != nil {
		return err
	}

	scopes, err := DiscoverScopes(ctx, r, opt.RepoRoot, opt.Dir, buildkitHost)
	if err != nil {
		return err
	}

	activatedPluginTasks := groupContainerTasks(scopes)

	for _, scope := range scopes {
		dir := scope.Dir()
		rel, err := filepath.Rel(opt.RepoRoot, dir)
		name := rel
		if err != nil {
			name = dir
		} else if rel == "." {
			name = "(repo root)"
		}
		fmt.Printf("%s\n", name)

		var all []tasks.Task

		// Build tasks
		cfg, err := config.Load(opt.RepoRoot)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		naming := images.ResolveImageNaming(cfg)
		buildTasks, err := images.BuildTasks(scope.SourceTree(), dir, false, "", naming)
		if err == nil && buildTasks != nil {
			all = append(all, buildTasks)
		}
		nonContainerBuild := filterNonContainerTasks(scope.BuildTasks)
		if len(nonContainerBuild) > 0 {
			all = append(all, &tasks.Group{Name: "build-scripts", Tasks: nonContainerBuild})
		}

		// Test tasks
		nonContainerTest := filterNonContainerTasks(scope.TestTasks)
		if len(nonContainerTest) > 0 {
			all = append(all, &tasks.Group{Name: "test-tasks", Tasks: nonContainerTest})
		}

		// E2E tasks
		nonContainerE2E := filterNonContainerTasks(scope.E2ETasks)
		if len(nonContainerE2E) > 0 {
			all = append(all, &tasks.Group{Name: "e2e-tasks", Tasks: nonContainerE2E})
		}

		// Lint tasks
		nonContainerLint := filterNonContainerTasks(scope.LintTasks)
		if len(nonContainerLint) > 0 {
			all = append(all, &tasks.Group{Name: "lint-tasks", Tasks: nonContainerLint})
		}

		// Format tasks
		nonContainerFormat := filterNonContainerTasks(scope.FormatTasks)
		if len(nonContainerFormat) > 0 {
			all = append(all, &tasks.Group{Name: "format-tasks", Tasks: nonContainerFormat})
		}

		if pluginTasks := activatedPluginTasks[dir]; len(pluginTasks) > 0 {
			all = append(all, pluginTasks...)
			delete(activatedPluginTasks, dir)
		}

		for _, task := range all {
			printTask(task, 1)
		}
	}

	var otherDirectories []string
	for directory := range activatedPluginTasks {
		otherDirectories = append(otherDirectories, directory)
	}
	sort.Strings(otherDirectories)

	for _, directory := range otherDirectories {
		rel, err := filepath.Rel(opt.RepoRoot, directory)
		name := rel
		if err != nil {
			name = directory
		}
		fmt.Printf("%s\n", filepath.ToSlash(name))
		for _, task := range activatedPluginTasks[directory] {
			printTask(task, 1)
		}
	}

	return nil
}

func printTask(t tasks.Task, indent int) {
	fmt.Printf("%s- %s\n", strings.Repeat("  ", indent), t.GetName())
	for _, child := range t.GetChildren() {
		printTask(child, indent+1)
	}
}

func filterNonContainerTasks(taskList []tasks.Task) []tasks.Task {
	var result []tasks.Task
	for _, t := range taskList {
		if _, ok := t.(*plugins.ContainerTask); !ok {
			result = append(result, t)
		}
	}
	return result
}

func groupContainerTasks(scopes []*tasks.TaskContext) map[string][]tasks.Task {
	tasksByDirectory := make(map[string]map[string][]tasks.Task)
	for _, scope := range scopes {
		taskLists := [][]tasks.Task{
			scope.BuildTasks,
			scope.TestTasks,
			scope.LintTasks,
			scope.FormatTasks,
			scope.GenerateTasks,
			scope.E2ETasks,
		}
		for _, taskList := range taskLists {
			for _, task := range taskList {
				containerTask, ok := task.(*plugins.ContainerTask)
				if !ok {
					continue
				}
				groupName := containerTask.TaskMetadata.Verb + "-tasks"
				if tasksByDirectory[containerTask.Dir] == nil {
					tasksByDirectory[containerTask.Dir] = make(map[string][]tasks.Task)
				}
				tasksByDirectory[containerTask.Dir][groupName] = append(tasksByDirectory[containerTask.Dir][groupName], containerTask)
			}
		}
	}

	result := make(map[string][]tasks.Task)
	for directory, groupMap := range tasksByDirectory {
		var groupNames []string
		for groupName := range groupMap {
			groupNames = append(groupNames, groupName)
		}
		sort.Strings(groupNames)

		var groups []tasks.Task
		for _, groupName := range groupNames {
			taskList := groupMap[groupName]
			sort.Slice(taskList, func(i, j int) bool { return taskList[i].GetName() < taskList[j].GetName() })
			groups = append(groups, &tasks.Group{Name: groupName, Tasks: taskList})
		}
		result[directory] = groups
	}
	return result
}
