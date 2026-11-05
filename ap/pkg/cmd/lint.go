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

// LintOptions holds the configuration for the "lint" command.
type LintOptions struct {
	*RootOptions
	WriteBaseline bool
}

// BuildLintCommand constructs the cobra command for "lint".
func BuildLintCommand(rootOpt *RootOptions) *cobra.Command {
	opt := LintOptions{
		RootOptions: rootOpt,
	}

	cmd := &cobra.Command{
		Use:   "lint [path...]",
		Short: "Run linting tasks (vet, govulncheck, prlinter)",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opt.Resolve(args); err != nil {
				return err
			}
			return RunLint(cmd.Context(), opt)
		},
	}

	cmd.Flags().BoolVar(&opt.WriteBaseline, "write-baseline", false, "Write baseline for linters with findings")

	cmd.AddCommand(BuildAnalyzeCommand())
	cmd.AddCommand(BuildDroppedErrorsCommand(rootOpt))

	return cmd
}

// RunLint executes the business logic for the "lint" command.
func RunLint(ctx context.Context, opt LintOptions) error {
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

	var allTasks []tasks.Task
	for _, scope := range scopes {
		if len(scope.LintTasks) > 0 {
			group := &tasks.Group{
				Name:            fmt.Sprintf("lint-%s", filepath.Base(scope.Dir())),
				Tasks:           scope.LintTasks,
				ContinueOnError: true,
			}
			allTasks = append(allTasks, group)
		}
	}

	if opt.WriteBaseline {
		for _, task := range allTasks {
			golang.SetWriteBaseline(task, true)
		}
	}

	return tasks.Run(ctx, tasks.NewTaskContext(opt.RepoRoot, opt.RepoRoot, r), allTasks, tasks.RunOptions{DryRun: opt.DryRun, ContinueOnError: true})
}
