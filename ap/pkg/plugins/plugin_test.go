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

	tree, err := sourcetree.Open(tempDir)
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
