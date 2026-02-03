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

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sandbox"
	"github.com/spf13/cobra"
)

// AlphaOptions holds the configuration for the "alpha" command.
type AlphaOptions struct {
	*RootOptions
}

// BuildAlphaCommand constructs the cobra command for "alpha".
func BuildAlphaCommand(rootOpt *RootOptions) *cobra.Command {
	opt := AlphaOptions{
		RootOptions: rootOpt,
	}

	cmd := &cobra.Command{
		Use:   "alpha",
		Short: "Experimental commands",
	}

	cmd.AddCommand(BuildSandboxCommand(&opt))

	return cmd
}

// SandboxOptions holds the configuration for the "sandbox" command.
type SandboxOptions struct {
	*AlphaOptions
}

// BuildSandboxCommand constructs the cobra command for "sandbox".
func BuildSandboxCommand(alphaOpt *AlphaOptions) *cobra.Command {
	opt := SandboxOptions{
		AlphaOptions: alphaOpt,
	}

	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Experimental sandbox command",
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunSandbox(cmd.Context(), opt, args)
		},
	}

	return cmd
}

// RunSandbox executes the business logic for the "sandbox" command.
func RunSandbox(ctx context.Context, opt SandboxOptions, args []string) error {
	if err := requireRepoRoot(opt.RootOptions); err != nil {
		return err
	}
	return sandbox.Run(ctx, opt.RepoRoot, args)
}
