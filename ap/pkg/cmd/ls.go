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
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/images"
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

	scopes, err := DiscoverScopes(r, opt.RepoRoot, opt.Dir)
	if err != nil {
		return err
	}

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
		if len(scope.BuildTasks) > 0 {
			all = append(all, &tasks.Group{Name: "build-scripts", Tasks: scope.BuildTasks})
		}

		// Test tasks
		if len(scope.TestTasks) > 0 {
			all = append(all, &tasks.Group{Name: "test-tasks", Tasks: scope.TestTasks})
		}

		// E2E tasks
		if len(scope.E2ETasks) > 0 {
			all = append(all, &tasks.Group{Name: "e2e-tasks", Tasks: scope.E2ETasks})
		}

		// Lint tasks
		if len(scope.LintTasks) > 0 {
			all = append(all, &tasks.Group{Name: "lint-tasks", Tasks: scope.LintTasks})
		}

		// Format tasks
		if len(scope.FormatTasks) > 0 {
			all = append(all, &tasks.Group{Name: "format-tasks", Tasks: scope.FormatTasks})
		}

		for _, task := range all {
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
