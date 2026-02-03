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

	"github.com/gke-labs/gke-labs-infra/ap/pkg/version"
	"github.com/spf13/cobra"
)

// VersionOptions holds the configuration for the "version" command.
type VersionOptions struct {
	*RootOptions
}

// BuildVersionCommand constructs the cobra command for "version".
func BuildVersionCommand(rootOpt *RootOptions) *cobra.Command {
	opt := VersionOptions{
		RootOptions: rootOpt,
	}

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunVersion(cmd.Context(), opt)
		},
	}

	return cmd
}

// RunVersion executes the business logic for the "version" command.
func RunVersion(ctx context.Context, opt VersionOptions) error {
	return version.Run(ctx, opt.RepoRoot)
}
