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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/portability"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/format"
	golang "github.com/gke-labs/gke-labs-infra/ap/pkg/go"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

// DiscoverScopes discovers all tasks for all given AP roots.
func DiscoverScopes(r *sourcetree.SourceTree, repoRoot string, apRoots []string) ([]*tasks.APScope, error) {
	var scopes []*tasks.APScope

	for _, apRoot := range apRoots {
		scope := tasks.NewScope(repoRoot, apRoot, r.Under(apRoot))

		// Discover e2e scripts
		e2eScripts, err := tasks.FindTaskScripts(scope.SourceTree, apRoot, tasks.WithPrefix("test-e2e"))
		if err != nil {
			return nil, fmt.Errorf("failed to discover e2e scripts in %s: %w", apRoot, err)
		}
		scope.E2ETasks = append(scope.E2ETasks, e2eScripts...)

		// Discover test scripts
		testScripts, err := tasks.FindTaskScripts(scope.SourceTree, apRoot, tasks.WithPrefix("test-"), tasks.WithExcludePrefix("test-e2e"))
		if err != nil {
			return nil, fmt.Errorf("failed to discover test scripts in %s: %w", apRoot, err)
		}
		scope.TestTasks = append(scope.TestTasks, testScripts...)

		// Discover build scripts
		buildScripts, err := tasks.FindTaskScripts(scope.SourceTree, apRoot, tasks.WithPrefix("build-"))
		if err != nil {
			return nil, fmt.Errorf("failed to discover build scripts in %s: %w", apRoot, err)
		}
		scope.BuildTasks = append(scope.BuildTasks, buildScripts...)

		// Discover lint scripts
		lintScripts, err := tasks.FindTaskScripts(scope.SourceTree, apRoot, tasks.WithPrefix("lint-"))
		if err != nil {
			return nil, fmt.Errorf("failed to discover lint scripts in %s: %w", apRoot, err)
		}
		scope.LintTasks = append(scope.LintTasks, lintScripts...)

		// Discover format tasks
		formatTasks, err := format.FormatTasks(scope.SourceTree, apRoot)
		if err != nil {
			return nil, fmt.Errorf("failed to discover format tasks in %s: %w", apRoot, err)
		}
		// formatTasks is a Task group
		if formatTasks != nil {
			scope.FormatTasks = append(scope.FormatTasks, formatTasks)
		}

		// Discover go e2e tasks
		sub := r.Under(apRoot)
		for _, d := range sub.Dirs() {
			if filepath.Base(d) != "tests" {
				continue
			}
			// If this tests directory belongs to a nested .ap root, skip it
			if r.APRootFor(d) != apRoot {
				continue
			}

			parentDir := filepath.Dir(d)
			e2eDir := filepath.Join(d, "e2e")
			if sub.HasGoTests(e2eDir) {
				buildDir := filepath.Join(repoRoot, ".build", "test-results", "go")
				if artifactsDir := os.Getenv("ARTIFACTS"); artifactsDir != "" {
					buildDir = filepath.Join(artifactsDir, "test-results", "go")
				}
				if err := os.MkdirAll(buildDir, 0755); err != nil {
					return nil, fmt.Errorf("failed to create build dir: %w", err)
				}
				rel, err := filepath.Rel(repoRoot, parentDir)
				if err != nil {
					return nil, fmt.Errorf("failed to determine relative path for %s: %w", parentDir, err)
				}
				name := rel
				if name == "." {
					name = "root"
				}
				filename := filepath.ToSlash(name)
				filename = strings.ReplaceAll(filename, "/", "-")
				resultFile := filepath.Join(buildDir, filename+"-e2e.json")
				scope.E2ETasks = append(scope.E2ETasks, &golang.GoE2eTask{
					Dir:        parentDir,
					Name:       name,
					ResultFile: resultFile,
				})
			}
		}

		// Go Test tasks
		goTestTasks, err := golang.TestTasks(scope.SourceTree, apRoot)
		if err != nil {
			return nil, fmt.Errorf("failed to discover test tasks in %s: %w", apRoot, err)
		}
		if goTestTasks != nil {
			scope.TestTasks = append(scope.TestTasks, goTestTasks)
		}

		// Go lint tasks
		goLintTasks, err := golang.LintTasks(scope.SourceTree, apRoot)
		if err != nil {
			return nil, fmt.Errorf("failed to discover lint tasks in %s: %w", apRoot, err)
		}
		if goLintTasks != nil {
			scope.LintTasks = append(scope.LintTasks, goLintTasks)
		}

		// Portability lint tasks
		portabilityTask, err := portability.NewTask(scope.SourceTree, apRoot, repoRoot)
		if err != nil {
			return nil, fmt.Errorf("failed to discover portability task in %s: %w", apRoot, err)
		}
		if portabilityTask != nil {
			scope.LintTasks = append(scope.LintTasks, portabilityTask)
		}

		scopes = append(scopes, scope)
	}

	return scopes, nil
}
