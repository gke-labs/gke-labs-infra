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

package golang

import (
	"context"
	"fmt"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

var (
	taskMetadataTest = plugins.TaskMetadata{
		Name:        "test",
		Verb:        "test",
		Description: "Run Go tests",
	}
	taskMetadataLint = plugins.TaskMetadata{
		Name:        "lint",
		Verb:        "lint",
		Description: "Run Go linters",
	}
)

// Plugin implements plugins.Plugin for Go tooling.
type Plugin struct{}

// Describe returns the manifest describing the Go plugin.
func (plugin *Plugin) Describe(ctx context.Context) (*plugins.PluginManifest, error) {
	return &plugins.PluginManifest{
		APIVersion: "ap.plugin/v1alpha1",
		Name:       "go",
		Activation: []string{"**/go.mod"},
		Tasks: []plugins.TaskMetadata{
			taskMetadataTest,
			taskMetadataLint,
		},
	}, nil
}

// DiscoverTasks discovers test and lint tasks for the Go module in dir.
func (plugin *Plugin) DiscoverTasks(ctx context.Context, dir string, scope *tasks.TaskContext) ([]plugins.PluginTask, error) {
	var discoveredTasks []plugins.PluginTask

	testTasks, err := TestTasks(scope.SourceTree(), dir)
	if err != nil {
		return nil, fmt.Errorf("failed to discover Go test tasks in %s: %w", dir, err)
	}
	if testTasks != nil {
		discoveredTasks = append(discoveredTasks, plugins.PluginTask{
			TaskMetadata: taskMetadataTest,
			Task:         testTasks,
		})
	}

	lintTasks, err := LintTasks(scope.SourceTree(), dir)
	if err != nil {
		return nil, fmt.Errorf("failed to discover Go lint tasks in %s: %w", dir, err)
	}
	if lintTasks != nil {
		discoveredTasks = append(discoveredTasks, plugins.PluginTask{
			TaskMetadata: taskMetadataLint,
			Task:         lintTasks,
		})
	}

	return discoveredTasks, nil
}
