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
	"os"
	"path/filepath"
	"sync"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
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
	TaskMetadata
	Task tasks.Task
}

var (
	registryMu sync.RWMutex
	registry   []Plugin
)

// Register registers an in-process plugin and returns an unregister function.
func Register(p Plugin) func() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = append(registry, p)
	return func() {
		Unregister(p)
	}
}

// Unregister removes a registered plugin.
func Unregister(p Plugin) {
	registryMu.Lock()
	defer registryMu.Unlock()
	for i, reg := range registry {
		if reg == p {
			registry = append(registry[:i], registry[i+1:]...)
			break
		}
	}
}

// Registered returns a copy of all registered in-process plugins.
func Registered() []Plugin {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Plugin, len(registry))
	copy(out, registry)
	return out
}

// Applies reports whether the plugin applies to the given directory.
func Applies(ctx context.Context, p Plugin, tree *sourcetree.SourceTree, dir string) (bool, error) {
	manifest, err := p.Describe(ctx)
	if err != nil {
		return false, err
	}
	return ManifestApplies(manifest, tree, dir), nil
}

// ManifestApplies checks whether manifest activation patterns match dir.
func ManifestApplies(manifest *PluginManifest, tree *sourcetree.SourceTree, dir string) bool {
	if manifest == nil {
		return false
	}
	for _, pat := range manifest.Activation {
		if tree != nil {
			for _, f := range tree.FilesInDir(dir) {
				matched, err := filepath.Match(pat, f.Name)
				if err == nil && matched {
					return true
				}
			}
			for _, f := range tree.Under(dir).Files() {
				matched, err := filepath.Match(pat, f.Name)
				if err == nil && matched {
					return true
				}
			}
		} else {
			var found bool
			err := filepath.Walk(dir, func(_ string, info os.FileInfo, walkErr error) error {
				if walkErr != nil || info == nil {
					return nil
				}
				matched, matchErr := filepath.Match(pat, info.Name())
				if matchErr == nil && matched {
					found = true
					return filepath.SkipAll
				}
				return nil
			})
			if err == nil && found {
				return true
			}
		}
	}
	return false
}

// DiscoverTasks returns all tasks from registered plugins that apply to dir.
func DiscoverTasks(ctx context.Context, scope *tasks.TaskContext, dir string) ([]PluginTask, error) {
	return TasksForVerb(ctx, "", scope, dir)
}

// TasksForVerb returns tasks from registered plugins matching verb in dir.
// If verb is empty, tasks for all verbs are returned.
func TasksForVerb(ctx context.Context, verb string, scope *tasks.TaskContext, dir string) ([]PluginTask, error) {
	var result []PluginTask
	tree := scope.SourceTree()
	for _, p := range Registered() {
		applies, err := Applies(ctx, p, tree, dir)
		if err != nil {
			return nil, err
		}
		if !applies {
			continue
		}
		pts, err := p.DiscoverTasks(ctx, dir, scope)
		if err != nil {
			return nil, err
		}
		for _, pt := range pts {
			if verb == "" || pt.Verb == verb {
				result = append(result, pt)
			}
		}
	}
	return result, nil
}
