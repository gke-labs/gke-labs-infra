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

package plugins

import (
	"context"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

// Plugin is the in-process plugin interface.
type Plugin interface {
	Describe(ctx context.Context) (*PluginManifest, error)
	DiscoverTasks(ctx context.Context, dir string, scope *tasks.TaskContext) ([]PluginTask, error)
}

// PluginManifest describes a plugin's capabilities and activation patterns.
type PluginManifest struct {
	APIVersion string         `json:"apiVersion"`
	Name       string         `json:"name"`
	Activation []string       `json:"activation"`
	Network    bool           `json:"network,omitempty"`
	Tasks      []TaskMetadata `json:"tasks"`
}

// TaskMetadata describes a task provided by a plugin.
type TaskMetadata struct {
	Name        string   `json:"name"`
	Verb        string   `json:"verb"`
	Description string   `json:"description"`
	Outputs     []string `json:"outputs"`
}

// PluginTask pairs task metadata with an executable task.
type PluginTask struct {
	Plugin string
	TaskMetadata
	Task tasks.Task
}

// DiscoverTasks asks each plugin in availablePlugins for its tasks in dir.
func DiscoverTasks(ctx context.Context, availablePlugins []Plugin, scope *tasks.TaskContext, dir string) ([]PluginTask, error) {
	var discoveredTasks []PluginTask
	for _, plugin := range availablePlugins {
		manifest, err := plugin.Describe(ctx)
		if err != nil {
			return nil, err
		}
		pluginTasks, err := plugin.DiscoverTasks(ctx, dir, scope)
		if err != nil {
			return nil, err
		}
		for _, task := range pluginTasks {
			task.Plugin = manifest.Name
			discoveredTasks = append(discoveredTasks, task)
		}
	}
	return discoveredTasks, nil
}
