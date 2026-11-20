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

package plugins_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

type trivialPlugin struct {
	ran bool
}

func (plugin *trivialPlugin) Describe(ctx context.Context) (*plugins.PluginManifest, error) {
	return &plugins.PluginManifest{
		APIVersion: "ap.plugin/v1alpha1",
		Name:       "trivial",
		Activation: []string{"*.trivial"},
		Tasks: []plugins.TaskMetadata{
			{
				Name:        "trivial-task",
				Verb:        "test",
				Description: "Trivial test task",
			},
		},
	}, nil
}

func (plugin *trivialPlugin) DiscoverTasks(ctx context.Context, dir string, scope *tasks.TaskContext) ([]plugins.PluginTask, error) {
	return []plugins.PluginTask{
		{
			TaskMetadata: plugins.TaskMetadata{
				Name:        "trivial-task",
				Verb:        "test",
				Description: "Trivial test task",
			},
			Task: &tasks.FuncTask{
				Name: "trivial-task",
				RunFn: func(ctx context.Context, scope *tasks.TaskContext) error {
					plugin.ran = true
					return nil
				},
			},
		},
	}, nil
}

func TestTrivialPluginDiscoveryAndRun(t *testing.T) {
	secondPlugin := &trivialPlugin{}
	tempDir := t.TempDir()

	tree, err := sourcetree.Open(t.Context(), tempDir)
	if err != nil {
		t.Fatal(err)
	}
	scope := tasks.NewTaskContext(tempDir, tempDir, tree)

	pluginList := []plugins.Plugin{secondPlugin}
	discoveredTasks, err := plugins.DiscoverTasks(t.Context(), pluginList, scope, tempDir)
	if err != nil {
		t.Fatalf("DiscoverTasks failed: %v", err)
	}

	if len(discoveredTasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(discoveredTasks))
	}

	pluginTask := discoveredTasks[0]
	if pluginTask.Plugin != "trivial" {
		t.Fatalf("expected task plugin trivial, got %s", pluginTask.Plugin)
	}
	if pluginTask.Name != "trivial-task" {
		t.Fatalf("expected task name trivial-task, got %s", pluginTask.Name)
	}

	if err := pluginTask.Task.Run(t.Context(), scope); err != nil {
		t.Fatalf("task Run failed: %v", err)
	}
	if !secondPlugin.ran {
		t.Fatalf("expected trivial plugin task to run and set ran=true")
	}
}

func TestFindActivatedDirs(t *testing.T) {
	sourcetree.ResetCache()
	tempDir := t.TempDir()

	files := map[string]string{
		".git/config":                 "",
		"astro.config.mjs":            "export default {}",
		"docs/astro.config.mjs":       "export default {}",
		"site/nested/astro.config.ts": "export default {}",
		"ignored/astro.config.mjs":    "export default {}",
		"vendor/astro.config.mjs":     "export default {}",
	}

	for relativePath, content := range files {
		fullPath := filepath.Join(tempDir, relativePath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	tree, err := sourcetree.Open(t.Context(), tempDir, sourcetree.WithSkip("ignored/**"))
	if err != nil {
		t.Fatalf("sourcetree.Open failed: %v", err)
	}

	docsDir := filepath.Join(tempDir, "docs")
	nestedDir := filepath.Join(tempDir, "site", "nested")

	// 1. A bare name matches only at the top level.
	bareDirs, err := plugins.FindActivatedDirs(tree, "astro", []string{"astro.config.*"})
	if err != nil {
		t.Fatalf("FindActivatedDirs failed: %v", err)
	}
	if !reflect.DeepEqual(bareDirs, []string{tempDir}) {
		t.Errorf("bare name: expected [%s], got %v", tempDir, bareDirs)
	}

	// 2. **/name matches at depth and at the top level (honouring skips and vendor ignores).
	recursiveDirs, err := plugins.FindActivatedDirs(tree, "astro", []string{"**/astro.config.*"})
	if err != nil {
		t.Fatalf("FindActivatedDirs failed: %v", err)
	}
	expectedRecursive := []string{tempDir, docsDir, nestedDir}
	sort.Strings(expectedRecursive)
	if !reflect.DeepEqual(recursiveDirs, expectedRecursive) {
		t.Errorf("**/name: expected %v, got %v", expectedRecursive, recursiveDirs)
	}

	// 3. A directory-prefixed pattern matches only there.
	prefixedDirs, err := plugins.FindActivatedDirs(tree, "astro", []string{"docs/astro.config.*"})
	if err != nil {
		t.Fatalf("FindActivatedDirs failed: %v", err)
	}
	if !reflect.DeepEqual(prefixedDirs, []string{docsDir}) {
		t.Errorf("directory-prefixed: expected [%s], got %v", docsDir, prefixedDirs)
	}

	// 4. A pattern the matcher cannot parse is an error naming the plugin and the pattern.
	_, err = plugins.FindActivatedDirs(tree, "astro", []string{"[a-"})
	if err == nil || !strings.Contains(err.Error(), `"astro"`) || !strings.Contains(err.Error(), `"[a-"`) {
		t.Errorf("bad pattern: expected error naming plugin and pattern, got %v", err)
	}
}
