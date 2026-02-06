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

	golang "github.com/gke-labs/gke-labs-infra/ap/pkg/go"
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
		Use:   "test",
		Short: "Run tests",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
	if err := golang.Test(ctx, opt.RepoRoot); err != nil {
		return err
	}

	// Run test-* scripts (excluding test-e2e*)
	testTasks, err := tasks.FindTaskScripts(opt.RepoRoot, tasks.WithPrefix("test-"), tasks.WithExcludePrefix("test-e2e"))
	if err != nil {
		return fmt.Errorf("failed to discover test tasks: %w", err)
	}
	return tasks.Run(ctx, opt.RepoRoot, testTasks)
}
