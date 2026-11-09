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
	"io"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool"
	"github.com/spf13/cobra"
)

// ToolBuildctlOptions holds configuration for "tool buildctl".
type ToolBuildctlOptions struct {
	*RootOptions
	Args   []string
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
}

// BuildToolBuildctlCommand constructs the cobra command for "tool buildctl".
func BuildToolBuildctlCommand(rootOpt *RootOptions) *cobra.Command {
	t, ok := tool.Lookup("buildctl")
	if !ok {
		panic("tool buildctl not found in registry")
	}
	return buildToolSubcommand(rootOpt, t)
}

// RunToolBuildctl executes the buildctl tool with pinned version and settings.
func RunToolBuildctl(ctx context.Context, opt ToolBuildctlOptions) error {
	t, ok := tool.Lookup("buildctl")
	if !ok {
		return fmt.Errorf("tool buildctl not found in registry")
	}

	repoRoot := opt.RepoRoot

	return t.Run(ctx, tool.RunOptions{
		RepoRoot: repoRoot,
		Args:     opt.Args,
		DryRun:   opt.DryRun,
		Stdout:   opt.Stdout,
		Stderr:   opt.Stderr,
		Stdin:    opt.Stdin,
	})
}
