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

// ToolDockerBuildxOptions holds configuration for "tool docker-buildx".
type ToolDockerBuildxOptions struct {
	*RootOptions
	Args   []string
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
}

// BuildToolDockerBuildxCommand constructs the cobra command for "tool docker-buildx".
func BuildToolDockerBuildxCommand(rootOpt *RootOptions) *cobra.Command {
	t, ok := tool.Lookup("docker-buildx")
	if !ok {
		panic("tool docker-buildx not found in registry")
	}
	return buildToolSubcommand(rootOpt, t)
}

// RunToolDockerBuildx executes the docker-buildx tool with pinned version and settings.
func RunToolDockerBuildx(ctx context.Context, opt ToolDockerBuildxOptions) error {
	t, ok := tool.Lookup("docker-buildx")
	if !ok {
		return fmt.Errorf("tool docker-buildx not registered")
	}

	repoRoot := opt.APRoot
	if repoRoot == "" {
		repoRoot = opt.RepoRoot
	}

	return t.Run(ctx, tool.RunOptions{
		RepoRoot: repoRoot,
		Args:     opt.Args,
		DryRun:   opt.DryRun,
		Stdout:   opt.Stdout,
		Stderr:   opt.Stderr,
		Stdin:    opt.Stdin,
	})
}
