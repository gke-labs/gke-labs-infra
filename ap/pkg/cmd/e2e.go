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

	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"github.com/spf13/cobra"
)

// E2eOptions holds the configuration for the "e2e" command.
type E2eOptions struct {
	*RootOptions
}

// BuildE2eCommand constructs the cobra command for "e2e".
func BuildE2eCommand(rootOpt *RootOptions) *cobra.Command {
	opt := E2eOptions{
		RootOptions: rootOpt,
	}

	cmd := &cobra.Command{
		Use:   "e2e",
		Short: "Run e2e tests",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return RunE2e(cmd.Context(), opt)
		},
	}

	return cmd
}

// RunE2e executes the business logic for the "e2e" command.
func RunE2e(ctx context.Context, opt E2eOptions) error {
	if err := requireRepoRoot(opt.RootOptions); err != nil {
		return err
	}

	var allTasks []tasks.Task
	for _, apRoot := range opt.APRoots {
		// Run test-e2e* scripts
		e2eScripts, err := tasks.FindTaskScripts(apRoot, tasks.WithPrefix("test-e2e"))
		if err != nil {
			return fmt.Errorf("failed to discover e2e tasks in %s: %w", apRoot, err)
		}

		if len(e2eScripts) == 0 {
			continue
		}

		group := &tasks.Group{
			Name:  fmt.Sprintf("e2e-%s", filepath.Base(apRoot)),
			Tasks: e2eScripts,
		}
		allTasks = append(allTasks, group)
	}

	return tasks.Run(ctx, opt.RepoRoot, allTasks, tasks.RunOptions{DryRun: opt.DryRun})
}
