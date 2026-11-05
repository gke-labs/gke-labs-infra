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

// TestOptions holds the configuration for the "test" command.
type TestOptions struct {
	*RootOptions
}

// BuildTestCommand constructs the cobra command for "test".
func BuildTestCommand(rootOpt *RootOptions) *cobra.Command {
	opt := TestOptions{
		RootOptions: rootOpt,
	}

	cmd := &cobra.Command{
		Use:   "test [path...]",
		Short: "Run tests",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opt.Resolve(args); err != nil {
				return err
			}
			return RunTest(cmd.Context(), opt)
		},
	}

	return cmd
}

// RunTest executes the business logic for the "test" command.
func RunTest(ctx context.Context, opt TestOptions) error {
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
		if len(scope.TestTasks) > 0 {
			group := &tasks.Group{
				Name:            fmt.Sprintf("test-%s", filepath.Base(scope.Dir())),
				Tasks:           scope.TestTasks,
				ContinueOnError: true,
			}
			allTasks = append(allTasks, group)
		}
	}

	return tasks.Run(ctx, tasks.NewTaskContext(opt.RepoRoot, opt.RepoRoot, r), allTasks, tasks.RunOptions{DryRun: opt.DryRun, ContinueOnError: true})
}
