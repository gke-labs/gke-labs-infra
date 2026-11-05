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
	"sort"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/images"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"github.com/spf13/cobra"
)

// BuildOptions holds the configuration for the "build" command.
type BuildOptions struct {
	*RootOptions
	Targets      []string
	Push         bool
	BuildkitHost string
}

// Resolve sets the target paths for build.
func (opt *BuildOptions) Resolve(args []string) error {
	opt.Targets = args
	return nil
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

func isImageDir(r *sourcetree.SourceTree, dir string) (string, bool) {
	if filepath.Base(filepath.Dir(dir)) != "images" {
		return "", false
	}
	if !r.DirExists(dir) {
		return "", false
	}
	dockerfile := filepath.Join(dir, "Dockerfile")
	if !r.FileExists(dockerfile) {
		return "", false
	}
	name := filepath.Base(dir)
	if name == "" || name == "." || name == ".." || name == "images" {
		return "", false
	}
	return name, true
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

	currentDir := opt.Dir
	if currentDir == "" {
		currentDir = opt.RepoRoot
	}

	targets := opt.Targets
	if len(targets) == 0 {
		targets = []string{currentDir}
	}

	imageSet := make(map[string]bool)
	var allBuildTasks []tasks.Task
	seenBuildTasks := make(map[string]bool)

	for _, target := range targets {
		resolved := config.ResolveTargetPath(opt.RepoRoot, currentDir, target)
		resolved = strings.TrimSuffix(resolved, string(filepath.Separator)+"...")

		rel, err := filepath.Rel(opt.RepoRoot, resolved)
		if err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("target path %q is outside repository root", target)
		}

		if imgName, ok := isImageDir(r, resolved); ok {
			imageSet[imgName] = true
			continue
		}

		if !r.DirExists(resolved) && !r.FileExists(resolved) && resolved != opt.RepoRoot {
			return fmt.Errorf("no scopes found under %s", resolved)
		}

		scopes, err := DiscoverScopes(r, opt.RepoRoot, resolved)
		if err != nil {
			return err
		}
		if len(scopes) == 0 {
			return fmt.Errorf("no scopes found under %s", resolved)
		}

		subImages, err := images.FindImageNames(r, resolved)
		if err != nil {
			return err
		}

		var scopeBuildTasks []tasks.Task
		for _, scope := range scopes {
			scopeBuildTasks = append(scopeBuildTasks, scope.BuildTasks...)
		}

		if len(subImages) == 0 && len(scopeBuildTasks) == 0 {
			return fmt.Errorf("nothing to build under %s: no images/*/Dockerfile and no dev/tasks/build-* scripts", resolved)
		}

		for _, img := range subImages {
			imageSet[img] = true
		}
		for _, bt := range scopeBuildTasks {
			if !seenBuildTasks[bt.GetName()] {
				seenBuildTasks[bt.GetName()] = true
				allBuildTasks = append(allBuildTasks, bt)
			}
		}
	}

	var allImageNames []string
	for img := range imageSet {
		allImageNames = append(allImageNames, img)
	}
	sort.Strings(allImageNames)

	cfg, err := config.Load(opt.RepoRoot)
	if err != nil {
		return err
	}
	naming := images.ResolveImageNaming(cfg)

	groupNameDir := currentDir
	if len(targets) == 1 {
		groupNameDir = config.ResolveTargetPath(opt.RepoRoot, currentDir, targets[0])
		groupNameDir = strings.TrimSuffix(groupNameDir, string(filepath.Separator)+"...")
	}
	group := &tasks.Group{
		Name: fmt.Sprintf("build-%s", filepath.Base(groupNameDir)),
	}

	if len(allImageNames) > 0 {
		imageTasks, err := images.BuildTasksForImages(r, opt.RepoRoot, opt.Push, buildkitHost, allImageNames, naming)
		if err != nil {
			return err
		}
		if imageTasks != nil && len(imageTasks.GetChildren()) > 0 {
			group.Tasks = append(group.Tasks, imageTasks)
		}
	}

	group.Tasks = append(group.Tasks, allBuildTasks...)

	var allTasks []tasks.Task
	if len(group.Tasks) > 0 {
		allTasks = append(allTasks, group)
	}

	if len(allTasks) == 0 {
		return fmt.Errorf("nothing to build under %s: no images/*/Dockerfile and no dev/tasks/build-* scripts", groupNameDir)
	}

	return tasks.Run(ctx, tasks.NewScope(opt.RepoRoot, opt.RepoRoot, r), allTasks, tasks.RunOptions{DryRun: opt.DryRun})
}
