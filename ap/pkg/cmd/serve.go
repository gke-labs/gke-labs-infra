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

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sandbox"
	"github.com/spf13/cobra"
)

// ServeOptions holds the configuration for the "serve" command.
type ServeOptions struct {
	*RootOptions
	ServeRoot string
	Port      int
}

// BuildServeCommand constructs the cobra command for "serve".
func BuildServeCommand(rootOpt *RootOptions) *cobra.Command {
	opt := ServeOptions{
		RootOptions: rootOpt,
	}

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the sandbox server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opt.ServeRoot == "" {
				opt.ServeRoot = opt.RepoRoot
			}
			if opt.ServeRoot == "" {
				return fmt.Errorf("--root is required if not run inside a git repository")
			}
			return RunServe(cmd.Context(), opt)
		},
	}

	cmd.Flags().StringVar(&opt.ServeRoot, "root", "", "Root directory for the sandbox server (defaults to repo root)")
	cmd.Flags().IntVar(&opt.Port, "port", 50051, "Port to listen on")

	return cmd
}

// RunServe executes the business logic for the "serve" command.
func RunServe(ctx context.Context, opt ServeOptions) error {
	return sandbox.Serve(ctx, opt.ServeRoot, opt.Port)
}
