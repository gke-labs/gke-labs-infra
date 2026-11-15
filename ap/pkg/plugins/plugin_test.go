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
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

type trivialPlugin struct {
	ran bool
}

func (p *trivialPlugin) Describe(ctx context.Context) (*plugins.PluginManifest, error) {
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

func (p *trivialPlugin) DiscoverTasks(ctx context.Context, dir string, scope *tasks.TaskContext) ([]plugins.PluginTask, error) {
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
					p.ran = true
					return nil
				},
			},
		},
	}, nil
}

func TestTrivialPluginDiscoveryAndRun(t *testing.T) {
	tp := &trivialPlugin{}
	cleanup := plugins.Register(tp)
	defer cleanup()

	tmpDir := t.TempDir()
	markerFile := filepath.Join(tmpDir, "sample.trivial")
	if err := os.WriteFile(markerFile, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	tree, err := sourcetree.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	scope := tasks.NewTaskContext(tmpDir, tmpDir, tree)

	// Verify plugin discovery for the "test" verb
	discovered, err := plugins.TasksForVerb(t.Context(), "test", scope, tmpDir)
	if err != nil {
		t.Fatalf("TasksForVerb failed: %v", err)
	}

	var trivialTask tasks.Task
	for _, pt := range discovered {
		if pt.Name == "trivial-task" {
			trivialTask = pt.Task
			break
		}
	}
	if trivialTask == nil {
		t.Fatalf("trivial-task was not discovered in %v", discovered)
	}

	// Run the discovered task
	if err := trivialTask.Run(t.Context(), scope); err != nil {
		t.Fatalf("trivial task Run failed: %v", err)
	}
	if !tp.ran {
		t.Fatalf("expected trivial task to execute and set ran=true")
	}

	// Verify plugin does not activate in an empty directory
	emptyDir := t.TempDir()
	emptyTree, err := sourcetree.Open(emptyDir)
	if err != nil {
		t.Fatal(err)
	}
	emptyScope := tasks.NewTaskContext(emptyDir, emptyDir, emptyTree)

	emptyDiscovered, err := plugins.TasksForVerb(t.Context(), "test", emptyScope, emptyDir)
	if err != nil {
		t.Fatalf("TasksForVerb failed on emptyDir: %v", err)
	}
	for _, pt := range emptyDiscovered {
		if pt.Name == "trivial-task" {
			t.Fatalf("trivial-task should not be discovered in emptyDir")
		}
	}
}
