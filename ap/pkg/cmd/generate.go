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

	"github.com/gke-labs/gke-labs-infra/ap/pkg/format"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/generate"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"github.com/spf13/cobra"
)

// GenerateOptions holds the configuration for the "generate" command.
type GenerateOptions struct {
	*RootOptions
	Verify         bool
	AuditGenerated bool
	Strict         bool
}

// BuildGenerateCommand constructs the cobra command for "generate".
func BuildGenerateCommand(rootOpt *RootOptions) *cobra.Command {
	opt := GenerateOptions{
		RootOptions: rootOpt,
	}

	cmd := &cobra.Command{
		Use:   "generate [path...]",
		Short: "Run generation tasks",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opt.Resolve(args); err != nil {
				return err
			}
			return RunGenerate(cmd.Context(), opt)
		},
	}

	cmd.Flags().BoolVar(&opt.Verify, "verify", false, "Verify generator coverage by perturbing outputs and verifying restoration")
	cmd.Flags().BoolVar(&opt.AuditGenerated, "audit-generated", false, "Audit repository for generated files that lack a generated header (runs against HEAD in a scratch worktree; uncommitted changes are not audited)")
	cmd.Flags().BoolVar(&opt.Strict, "strict", false, "Exit non-zero if audit mode detects generated files lacking a header")

	return cmd
}

// BuildVerifyGenerateCommand constructs the cobra command for "verify-generate".
func BuildVerifyGenerateCommand(rootOpt *RootOptions) *cobra.Command {
	opt := GenerateOptions{
		RootOptions: rootOpt,
		Verify:      true,
	}

	cmd := &cobra.Command{
		Use:   "verify-generate [path...]",
		Short: "Verify generator coverage by perturbing outputs and proving generators restore them",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opt.Resolve(args); err != nil {
				return err
			}
			return RunGenerate(cmd.Context(), opt)
		},
	}

	return cmd
}

// RunGenerate executes the business logic for the "generate" command.
func RunGenerate(ctx context.Context, opt GenerateOptions) error {
	if err := requireRepoRoot(opt.RootOptions); err != nil {
		return err
	}

	r, err := opt.GetSourceTree()
	if err != nil {
		return err
	}

	var allTasks []tasks.Task

	scopes, err := DiscoverScopes(r, opt.RepoRoot, opt.Dir)
	if err != nil {
		return err
	}
	generateTasks, err := generate.GenerateTasks(opt.RepoRoot, scopes)
	if err != nil {
		return err
	}
	allTasks = append(allTasks, generateTasks)

	for _, scope := range scopes {
		formatTasks, err := format.FormatTasks(scope.SourceTree(), scope.Dir())
		if err != nil {
			return err
		}
		allTasks = append(allTasks, formatTasks)
	}

	if opt.Verify {
		return generate.RunVerify(ctx, opt.RepoRoot, scopes, allTasks, opt.DryRun)
	}

	if opt.AuditGenerated {
		return generate.RunAudit(ctx, opt.RepoRoot, scopes, generate.AuditOptions{
			Strict: opt.Strict,
			DryRun: opt.DryRun,
		})
	}

	return tasks.Run(ctx, tasks.NewTaskContext(opt.RepoRoot, opt.RepoRoot, r), allTasks, tasks.RunOptions{DryRun: opt.DryRun})
}
