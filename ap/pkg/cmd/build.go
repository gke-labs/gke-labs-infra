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

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/images"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"github.com/spf13/cobra"
)

// BuildOptions holds the configuration for the "build" command.
type BuildOptions struct {
	*RootOptions
	Push         bool
	BuildkitHost string
}

// BuildBuildCommand constructs the cobra command for "build".
func BuildBuildCommand(rootOpt *RootOptions) *cobra.Command {
	opt := BuildOptions{
		RootOptions: rootOpt,
	}

	cmd := &cobra.Command{
		Use:   "build [path...]",
		Short: "Build artifacts",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opt.Resolve(args); err != nil {
				return err
			}
			return RunBuild(cmd.Context(), opt)
		},
	}

	cmd.Flags().BoolVar(&opt.Push, "push", false, "Push images to registry")
	cmd.Flags().StringVar(&opt.BuildkitHost, "buildkit-host", "", "Buildkit host to use (e.g. k8s://namespace/service)")

	return cmd
}

// RunBuild executes the business logic for the "build" command.
func RunBuild(ctx context.Context, opt BuildOptions) error {
	if err := requireRepoRoot(opt.RootOptions); err != nil {
		return err
	}

	buildkitHost, err := images.ResolveBuildkitHost(opt.BuildkitHost)
	if err != nil {
		return err
	}

	r, err := opt.GetSourceTree()
	if err != nil {
		return err
	}

	targetDir := opt.RepoRoot
	if len(opt.APRoots) == 1 && opt.APRoots[0] != "" {
		targetDir = opt.APRoots[0]
	}

	scopes, err := DiscoverScopes(r, opt.RepoRoot, opt.APRoots)
	if err != nil {
		return err
	}
	if len(scopes) == 0 {
		return fmt.Errorf("no scopes found under %s", targetDir)
	}

	cfg, err := config.Load(opt.RepoRoot)
	if err != nil {
		return err
	}
	naming := images.ResolveImageNaming(cfg)

	var allTasks []tasks.Task
	for _, scope := range scopes {
		group := &tasks.Group{
			Name: fmt.Sprintf("build-%s", filepath.Base(scope.Dir)),
		}

		imageTasks, err := images.BuildTasks(scope.SourceTree, scope.Dir, opt.Push, buildkitHost, naming)
		if err != nil {
			return err
		}
		if imageTasks != nil && len(imageTasks.GetChildren()) > 0 {
			group.Tasks = append(group.Tasks, imageTasks)
		}

		group.Tasks = append(group.Tasks, scope.BuildTasks...)

		if len(group.Tasks) > 0 {
			allTasks = append(allTasks, group)
		}
	}

	if len(allTasks) == 0 {
		return fmt.Errorf("nothing to build under %s: no images/*/Dockerfile and no dev/tasks/build-* scripts", targetDir)
	}

	return tasks.Run(ctx, tasks.NewScope(opt.RepoRoot, opt.RepoRoot, r), allTasks, tasks.RunOptions{DryRun: opt.DryRun})
}
