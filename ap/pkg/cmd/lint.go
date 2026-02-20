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

	golang "github.com/gke-labs/gke-labs-infra/ap/pkg/go"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/prlinter"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"github.com/spf13/cobra"
)

// LintOptions holds the configuration for the "lint" command.
type LintOptions struct {
	*RootOptions
}

// BuildLintCommand constructs the cobra command for "lint".
func BuildLintCommand(rootOpt *RootOptions) *cobra.Command {
	opt := LintOptions{
		RootOptions: rootOpt,
	}

	cmd := &cobra.Command{
		Use:   "lint",
		Short: "Run linting tasks (vet, govulncheck, prlinter)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return RunLint(cmd.Context(), opt)
		},
	}

	cmd.AddCommand(BuildUnusedCommand())
	cmd.AddCommand(BuildTestContextCommand())

	return cmd
}

// RunLint executes the business logic for the "lint" command.
func RunLint(ctx context.Context, opt LintOptions) error {
	if err := requireRepoRoot(opt.RootOptions); err != nil {
		return err
	}

	var allTasks []tasks.Task

	prTask, err := prlinter.LintTasks(opt.RepoRoot)
	if err != nil {
		return err
	}
	allTasks = append(allTasks, prTask)

	for _, apRoot := range opt.APRoots {
		group := &tasks.Group{
			Name: fmt.Sprintf("lint-%s", filepath.Base(apRoot)),
		}

		goTasks, err := golang.LintTasks(apRoot)
		if err != nil {
			return err
		}
		group.Tasks = append(group.Tasks, goTasks)

		allTasks = append(allTasks, group)
	}

	return tasks.Run(ctx, opt.RepoRoot, allTasks, tasks.RunOptions{DryRun: opt.DryRun})
}
