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
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"github.com/spf13/cobra"
)

// E2eOptions holds the configuration for the "e2e" command.
type E2eOptions struct {
	*RootOptions
	Shard   string
	Run     string
	Timeout string
}

// BuildE2eCommand constructs the cobra command for "e2e".
func BuildE2eCommand(rootOpt *RootOptions) *cobra.Command {
	opt := E2eOptions{
		RootOptions: rootOpt,
	}

	cmd := &cobra.Command{
		Use:   "e2e [path...]",
		Short: "Run e2e tests",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opt.Resolve(args); err != nil {
				return err
			}
			return RunE2e(cmd.Context(), opt)
		},
	}

	cmd.Flags().StringVar(&opt.Shard, "shard", "", "Shard selector in format i/n (e.g. 1/3) (env: AP_E2E_SHARD)")
	cmd.Flags().StringVar(&opt.Run, "run", "", "Run tests matching regular expression (env: AP_E2E_RUN)")
	cmd.Flags().StringVar(&opt.Timeout, "timeout", "", "Timeout for tests (env: AP_E2E_TIMEOUT)")

	return cmd
}

// RunE2e executes the business logic for the "e2e" command.
func RunE2e(ctx context.Context, opt E2eOptions) error {
	if err := requireRepoRoot(opt.RootOptions); err != nil {
		return err
	}

	r, err := opt.GetRepo()
	if err != nil {
		return err
	}

	scopes, err := DiscoverScopes(r, opt.RepoRoot, opt.APRoots)
	if err != nil {
		return err
	}

	var allTasks []tasks.Task
	for _, scope := range scopes {
		if len(scope.E2ETasks) > 0 {
			for _, task := range scope.E2ETasks {
				if goE2e, ok := task.(*golang.GoE2eTask); ok {
					if opt.Shard != "" {
						goE2e.Shard = opt.Shard
					}
					if opt.Run != "" {
						goE2e.RunPattern = opt.Run
					}
					if opt.Timeout != "" {
						goE2e.Timeout = opt.Timeout
					}
				}
			}
			group := &tasks.Group{
				Name:  fmt.Sprintf("e2e-%s", filepath.Base(scope.Dir)),
				Tasks: scope.E2ETasks,
			}
			allTasks = append(allTasks, group)
		}
	}

	return tasks.Run(ctx, &tasks.APScope{RepoRoot: opt.RepoRoot, Dir: opt.RepoRoot, Repo: r}, allTasks, tasks.RunOptions{DryRun: opt.DryRun})
}
