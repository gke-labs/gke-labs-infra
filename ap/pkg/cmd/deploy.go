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
	"os"
	"path/filepath"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/images"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/k8s"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"github.com/spf13/cobra"
)

// DeployOptions holds the configuration for the "deploy" command.
type DeployOptions struct {
	*RootOptions
}

// BuildDeployCommand constructs the cobra command for "deploy".
func BuildDeployCommand(rootOpt *RootOptions) *cobra.Command {
	opt := DeployOptions{
		RootOptions: rootOpt,
	}

	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy artifacts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return RunDeploy(cmd.Context(), opt)
		},
	}

	return cmd
}

// RunDeploy executes the business logic for the "deploy" command.
func RunDeploy(ctx context.Context, opt DeployOptions) error {
	if err := requireRepoRoot(opt.RootOptions); err != nil {
		return err
	}

	if os.Getenv("IMAGE_PREFIX") == "" {
		return fmt.Errorf("IMAGE_PREFIX is not set; it is required for deploy")
	}

	var allTasks []tasks.Task
	for _, apRoot := range opt.APRoots {
		group := &tasks.Group{
			Name: fmt.Sprintf("deploy-%s", filepath.Base(apRoot)),
		}

		// Deploy typically also builds
		buildTasks, err := images.BuildTasks(apRoot, true)
		if err != nil {
			return fmt.Errorf("build failed during deploy for %s: %w", apRoot, err)
		}
		group.Tasks = append(group.Tasks, buildTasks)

		deployTasks, err := k8s.DeployTasks(apRoot)
		if err != nil {
			return fmt.Errorf("deploy failed for %s: %w", apRoot, err)
		}
		group.Tasks = append(group.Tasks, deployTasks)

		allTasks = append(allTasks, group)
	}

	return tasks.Run(ctx, opt.RepoRoot, allTasks, tasks.RunOptions{DryRun: opt.DryRun})
}
