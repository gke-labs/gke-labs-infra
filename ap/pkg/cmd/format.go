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
	"path/filepath"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/format"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"github.com/spf13/cobra"
)

// FormatOptions holds the configuration for the "format" command.
type FormatOptions struct {
	*RootOptions
}

// BuildFormatCommand constructs the cobra command for "format".
func BuildFormatCommand(rootOpt *RootOptions) *cobra.Command {
	opt := FormatOptions{
		RootOptions: rootOpt,
	}

	cmd := &cobra.Command{
		Use:     "format",
		Aliases: []string{"fmt"},
		Short:   "Run formatting tasks",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return RunFormat(cmd.Context(), opt)
		},
	}

	return cmd
}

// RunFormat executes the business logic for the "format" command.
func RunFormat(ctx context.Context, opt FormatOptions) error {
	if err := requireRepoRoot(opt.RootOptions); err != nil {
		return err
	}

	var allTasks []tasks.Task
	for _, apRoot := range opt.APRoots {
		group, err := format.FormatTasks(apRoot)
		if err != nil {
			return err
		}
		if g, ok := group.(*tasks.Group); ok {
			g.Name = fmt.Sprintf("format-%s", filepath.Base(apRoot))
		}
		allTasks = append(allTasks, group)
	}

	return tasks.Run(ctx, opt.RepoRoot, allTasks, tasks.RunOptions{DryRun: opt.DryRun})
}
