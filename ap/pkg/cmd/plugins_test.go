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
	"os"
	"path/filepath"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

type trivialInProcessPlugin struct {
	ran bool
}

func (p *trivialInProcessPlugin) Describe(ctx context.Context) (*plugins.PluginManifest, error) {
	return &plugins.PluginManifest{
		APIVersion: "ap.plugin/v1alpha1",
		Name:       "trivial",
		Activation: []string{"*.trivial"},
		Tasks: []plugins.TaskMetadata{
			{
				Name:        "trivial-test",
				Verb:        "test",
				Description: "Trivial test task",
			},
		},
	}, nil
}

func (p *trivialInProcessPlugin) DiscoverTasks(ctx context.Context, dir string, scope *tasks.TaskContext) ([]plugins.PluginTask, error) {
	return []plugins.PluginTask{
		{
			TaskMetadata: plugins.TaskMetadata{
				Name:        "trivial-test",
				Verb:        "test",
				Description: "Trivial test task",
			},
			Task: &tasks.FuncTask{
				Name: "trivial-test",
				RunFn: func(ctx context.Context, scope *tasks.TaskContext) error {
					p.ran = true
					return nil
				},
			},
		},
	}, nil
}

func TestDiscoverScopes_TrivialPlugin(t *testing.T) {
	tp := &trivialInProcessPlugin{}
	cleanup := plugins.Register(tp)
	defer cleanup()

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "marker.trivial"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	r, err := sourcetree.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	scopes, err := DiscoverScopes(r, tmpDir, tmpDir)
	if err != nil {
		t.Fatalf("DiscoverScopes failed: %v", err)
	}
	if len(scopes) != 1 {
		t.Fatalf("expected 1 scope, got %d", len(scopes))
	}

	var found tasks.Task
	for _, task := range scopes[0].TestTasks {
		if task.GetName() == "trivial-test" {
			found = task
			break
		}
	}
	if found == nil {
		t.Fatalf("trivial-test task was not discovered in scope.TestTasks")
	}

	if err := found.Run(t.Context(), scopes[0]); err != nil {
		t.Fatalf("failed to run trivial task: %v", err)
	}
	if !tp.ran {
		t.Fatalf("expected trivial task to run and set ran=true")
	}
}
