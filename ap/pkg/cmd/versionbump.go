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

	"github.com/gke-labs/gke-labs-infra/ap/pkg/versionbump"
	"github.com/spf13/cobra"
)

// VersionBumpOptions holds the configuration for the "versionbump" command.
type VersionBumpOptions struct {
	*RootOptions
}

// BuildVersionBumpCommand constructs the cobra command for "versionbump".
func BuildVersionBumpCommand(rootOpt *RootOptions) *cobra.Command {
	opt := VersionBumpOptions{
		RootOptions: rootOpt,
	}

	cmd := &cobra.Command{
		Use:   "versionbump",
		Short: "Bump project versions (e.g. Go)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return RunVersionBump(cmd.Context(), opt)
		},
	}

	return cmd
}

// RunVersionBump executes the business logic for the "versionbump" command.
func RunVersionBump(ctx context.Context, opt VersionBumpOptions) error {
	if err := requireRepoRoot(opt.RootOptions); err != nil {
		return err
	}
	return versionbump.Run(ctx, opt.APRoot)
}
