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
	"github.com/spf13/cobra"
)

// GenerateOptions holds the configuration for the "generate" command.
type GenerateOptions struct {
	*RootOptions
}

// BuildGenerateCommand constructs the cobra command for "generate".
func BuildGenerateCommand(rootOpt *RootOptions) *cobra.Command {
	opt := GenerateOptions{
		RootOptions: rootOpt,
	}

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Run generation tasks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
	if err := generate.Run(ctx, opt.RepoRoot); err != nil {
		return err
	}
	return format.Run(ctx, opt.RepoRoot)
}
