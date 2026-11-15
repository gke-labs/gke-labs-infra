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
	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"github.com/spf13/cobra"
)

// LsOptions holds the configuration for the "ls" command.
type LsOptions struct {
	*RootOptions
}

// BuildLsCommand constructs the cobra command for "ls".
func BuildLsCommand(rootOpt *RootOptions) *cobra.Command {
	opt := LsOptions{
		RootOptions: rootOpt,
	}

	cmd := &cobra.Command{
		Use:   "ls [path...]",
		Short: "List AP roots",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opt.Resolve(args); err != nil {
				return err
			}
			return RunLs(cmd.Context(), opt)
		},
	}

	return cmd
}

// RunLs executes the business logic for the "ls" command.
func RunLs(ctx context.Context, opt LsOptions) error {
	if err := requireRepoRoot(opt.RootOptions); err != nil {
		return err
	}

	r, err := opt.GetSourceTree()
	if err != nil {
		return err
	}

	scopes, err := DiscoverScopes(ctx, r, opt.RepoRoot, opt.Dir)
	if err != nil {
		return err
	}

	pluginGroupsByDir, err := discoverPluginTasks(r, opt.RepoRoot, opt.Dir)
	if err != nil {
		return err
	}

	for _, scope := range scopes {
		dir := scope.Dir()
		rel, err := filepath.Rel(opt.RepoRoot, dir)
		name := rel
		if err != nil {
			name = dir
		} else if rel == "." {
			name = "(repo root)"
		}
		fmt.Printf("%s\n", name)

		var all []tasks.Task

		// Build tasks
		cfg, err := config.Load(opt.RepoRoot)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		naming := images.ResolveImageNaming(cfg)
		buildTasks, err := images.BuildTasks(scope.SourceTree(), dir, false, "", naming)
		if err == nil && buildTasks != nil {
			all = append(all, buildTasks)
		}
		if len(scope.BuildTasks) > 0 {
			all = append(all, &tasks.Group{Name: "build-scripts", Tasks: scope.BuildTasks})
		}
		if pBuild, ok := pluginGroup(pluginGroupsByDir[dir], "build-tasks"); ok {
			all = append(all, pBuild)
		}

		// Test tasks
		if len(scope.TestTasks) > 0 {
			all = append(all, &tasks.Group{Name: "test-tasks", Tasks: scope.TestTasks})
		}
		if pTest, ok := pluginGroup(pluginGroupsByDir[dir], "test-tasks"); ok {
			all = append(all, pTest)
		}

		// E2E tasks
		if len(scope.E2ETasks) > 0 {
			all = append(all, &tasks.Group{Name: "e2e-tasks", Tasks: scope.E2ETasks})
		}
		if pE2E, ok := pluginGroup(pluginGroupsByDir[dir], "e2e-tasks"); ok {
			all = append(all, pE2E)
		}

		// Lint tasks
		if len(scope.LintTasks) > 0 {
			all = append(all, &tasks.Group{Name: "lint-tasks", Tasks: scope.LintTasks})
		}
		if pLint, ok := pluginGroup(pluginGroupsByDir[dir], "lint-tasks"); ok {
			all = append(all, pLint)
		}

		// Format tasks
		if len(scope.FormatTasks) > 0 {
			all = append(all, &tasks.Group{Name: "format-tasks", Tasks: scope.FormatTasks})
		}
		if pFormat, ok := pluginGroup(pluginGroupsByDir[dir], "format-tasks"); ok {
			all = append(all, pFormat)
		}

		if pGen, ok := pluginGroup(pluginGroupsByDir[dir], "generate-tasks"); ok {
			all = append(all, pGen)
		}

		delete(pluginGroupsByDir, dir)

		for _, task := range all {
			printTask(task, 1)
		}
	}

	var otherDirs []string
	for d := range pluginGroupsByDir {
		otherDirs = append(otherDirs, d)
	}
	sort.Strings(otherDirs)

	for _, d := range otherDirs {
		rel, err := filepath.Rel(opt.RepoRoot, d)
		name := rel
		if err != nil {
			name = d
		}
		fmt.Printf("%s\n", filepath.ToSlash(name))
		for _, task := range pluginGroupsByDir[d] {
			printTask(task, 1)
		}
	}

	return nil
}

func discoverPluginTasks(r *sourcetree.SourceTree, repoRoot, targetDir string) (map[string][]tasks.Task, error) {
	pluginsCfg, err := config.LoadPluginsConfig(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to load plugin config: %w", err)
	}
	if pluginsCfg == nil || len(pluginsCfg.Plugins) == 0 {
		return nil, nil
	}

	var pNames []string
	for name := range pluginsCfg.Plugins {
		pNames = append(pNames, name)
	}
	sort.Strings(pNames)

	dirTasks := make(map[string]map[string][]tasks.Task)
	outputOwners := make(map[string]map[string]string)

	for _, pName := range pNames {
		decl := pluginsCfg.Plugins[pName]
		if !decl.IsEnabled() {
			continue
		}
		manifest, err := plugins.LoadCachedManifest("", plugins.CacheKey(decl))
		if err != nil {
			return nil, fmt.Errorf("plugin %q: %w", pName, err)
		}
		actDirs := plugins.FindActivatedDirs(r.Under(targetDir), manifest.Activation)
		for _, actDir := range actDirs {
			if dirTasks[actDir] == nil {
				dirTasks[actDir] = make(map[string][]tasks.Task)
			}
			if outputOwners[actDir] == nil {
				outputOwners[actDir] = make(map[string]string)
			}
			if len(manifest.Tasks) == 0 {
				dirTasks[actDir]["build-tasks"] = append(dirTasks[actDir]["build-tasks"], &tasks.FuncTask{Name: pName})
			}
			for _, t := range manifest.Tasks {
				taskName := fmt.Sprintf("%s-%s", pName, t.Name)
				for _, out := range t.Outputs {
					if prev, ok := outputOwners[actDir][out]; ok {
						return nil, fmt.Errorf("directory %q: conflicting output path %q declared by tasks %q and %q", actDir, out, prev, taskName)
					}
					outputOwners[actDir][out] = taskName
				}
				gName := t.Verb + "-tasks"
				dirTasks[actDir][gName] = append(dirTasks[actDir][gName], &tasks.FuncTask{Name: taskName})
			}
		}
	}

	res := make(map[string][]tasks.Task)
	verbOrder := []string{"build-tasks", "test-tasks", "e2e-tasks", "lint-tasks", "format-tasks", "generate-tasks"}
	for dir, gMap := range dirTasks {
		var groups []tasks.Task
		for _, gName := range verbOrder {
			if ts := gMap[gName]; len(ts) > 0 {
				sort.Slice(ts, func(i, j int) bool { return ts[i].GetName() < ts[j].GetName() })
				groups = append(groups, &tasks.Group{Name: gName, Tasks: ts})
			}
		}
		res[dir] = groups
	}
	return res, nil
}

func pluginGroup(groups []tasks.Task, name string) (tasks.Task, bool) {
	for _, g := range groups {
		if g.GetName() == name {
			return g, true
		}
	}
	return nil, false
}

func printTask(t tasks.Task, indent int) {
	fmt.Printf("%s- %s\n", strings.Repeat("  ", indent), t.GetName())
	for _, child := range t.GetChildren() {
		printTask(child, indent+1)
	}
}
