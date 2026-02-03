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

	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
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
		RunE: func(cmd *cobra.Command, args []string) error {
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
	// Run test-e2e* scripts
	e2eTasks, err := tasks.FindTaskScripts(opt.RepoRoot, tasks.WithPrefix("test-e2e"))
	if err != nil {
		return fmt.Errorf("failed to discover e2e tasks: %w", err)
	}

	if len(e2eTasks) == 0 {
		klog.Warning("No e2e tasks found (looking for dev/tasks/test-e2e*)")
		return nil
	}

	return tasks.Run(ctx, opt.RepoRoot, e2eTasks)
}
