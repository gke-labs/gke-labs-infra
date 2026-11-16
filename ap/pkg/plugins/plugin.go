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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

// CacheKey returns the describe cache key for imageRef.
func CacheKey(imageRef string) string {
	if index := strings.Index(imageRef, "@"); index != -1 {
		return imageRef[index+1:]
	}
	return imageRef
}

// LoadCachedManifest loads the PluginManifest for key from the cache directory.
func LoadCachedManifest(cacheDirectory, key string) (*PluginManifest, error) {
	sanitizedKey := strings.ReplaceAll(strings.ReplaceAll(key, ":", "-"), "/", "-")
	manifestPath := filepath.Join(cacheDirectory, "plugins", "describe", sanitizedKey+".json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("plugin manifest not cached for %s: %w", key, err)
	}
	var manifest PluginManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("error parsing cached plugin manifest %s: %w", manifestPath, err)
	}
	return &manifest, nil
}

// FindActivatedDirs finds all directories in tree where any file matches any pattern in patterns.
func FindActivatedDirs(tree *sourcetree.SourceTree, pluginName string, patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	for _, pattern := range patterns {
		if _, err := sourcetree.MatchPath(pattern, "", false); err != nil {
			return nil, fmt.Errorf("plugin %q: bad activation pattern %q: %w", pluginName, pattern, err)
		}
	}
	seen := make(map[string]bool)
	allDirectories := make([]string, 0, len(tree.Dirs())+1)
	allDirectories = append(allDirectories, tree.Root())
	seen[tree.Root()] = true
	for _, directory := range tree.Dirs() {
		if !seen[directory] {
			seen[directory] = true
			allDirectories = append(allDirectories, directory)
		}
	}

	var activatedDirectories []string
	for _, directory := range allDirectories {
		for _, file := range tree.FilesInDir(directory) {
			relPath, err := filepath.Rel(tree.RepoRoot(), file.Path)
			if err != nil {
				return nil, fmt.Errorf("plugin %q: %w", pluginName, err)
			}
			matched := false
			for _, pattern := range patterns {
				match, err := sourcetree.MatchPath(pattern, relPath, false)
				if err != nil {
					return nil, fmt.Errorf("plugin %q: bad activation pattern %q: %w", pluginName, pattern, err)
				}
				if match {
					activatedDirectories = append(activatedDirectories, directory)
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
	}
	sort.Strings(activatedDirectories)
	return activatedDirectories, nil
}
