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
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either expressGoogle LLC or its affiliates. All rights reserved.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"context"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/images"
	"github.com/spf13/cobra"
)

// BuildOptions holds the configuration for the "build" command.
type BuildOptions struct {
	*RootOptions
}

// BuildBuildCommand constructs the cobra command for "build".
func BuildBuildCommand(rootOpt *RootOptions) *cobra.Command {
	opt := BuildOptions{
		RootOptions: rootOpt,
	}

	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build artifacts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return RunBuild(cmd.Context(), opt)
		},
	}

	return cmd
}

// RunBuild executes the business logic for the "build" command.
func RunBuild(ctx context.Context, opt BuildOptions) error {
	if err := requireRepoRoot(opt.RootOptions); err != nil {
		return err
	}
	return images.Build(ctx, opt.RepoRoot)
}
