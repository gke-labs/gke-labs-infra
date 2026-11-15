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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
)

// PluginManifest represents the runner-neutral manifest returned by describe.
type PluginManifest struct {
	APIVersion string         `json:"apiVersion"`
	Name       string         `json:"name"`
	Activation []string       `json:"activation"`
	Network    bool           `json:"network,omitempty"`
	Tasks      []TaskMetadata `json:"tasks"`
}

// TaskMetadata represents task capabilities declared by a plugin.
type TaskMetadata struct {
	Name        string   `json:"name"`
	Verb        string   `json:"verb"`
	Description string   `json:"description"`
	Outputs     []string `json:"outputs"`
}

// CacheKey returns the describe cache key for decl.
func CacheKey(decl config.PluginDeclaration) string {
	if decl.Image != "" {
		if idx := strings.Index(decl.Image, "@"); idx != -1 {
			return decl.Image[idx+1:]
		}
		return decl.Image
	}
	return decl.Dockerfile
}

// LoadCachedManifest loads the PluginManifest for key from the AP cache directory.
func LoadCachedManifest(cacheDir, key string) (*PluginManifest, error) {
	if cacheDir == "" {
		c, err := fileutils.CacheDir()
		if err != nil {
			return nil, err
		}
		cacheDir = c
	}
	p := filepath.Join(cacheDir, "plugins", "describe", key+".json")
	data, err := os.ReadFile(p)
	if err != nil {
		sanitized := strings.ReplaceAll(strings.ReplaceAll(key, ":", "-"), "/", "-")
		p = filepath.Join(cacheDir, "plugins", "describe", sanitized+".json")
		if data, err = os.ReadFile(p); err != nil {
			return nil, fmt.Errorf("plugin manifest not cached for %s: %w", key, err)
		}
	}
	var m PluginManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("error parsing cached plugin manifest %s: %w", p, err)
	}
	return &m, nil
}

// FindActivatedDirs finds all directories in tree where any file matches any pattern in patterns.
func FindActivatedDirs(tree *sourcetree.SourceTree, patterns []string) []string {
	if tree == nil || len(patterns) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	allDirs := make([]string, 0, len(tree.Dirs())+1)
	allDirs = append(allDirs, tree.Root())
	seen[tree.Root()] = true
	for _, d := range tree.Dirs() {
		if !seen[d] {
			seen[d] = true
			allDirs = append(allDirs, d)
		}
	}

	var activated []string
	for _, dir := range allDirs {
		for _, f := range tree.FilesInDir(dir) {
			matched := false
			for _, pat := range patterns {
				m, err := filepath.Match(pat, f.Name)
				if err != nil {
					continue
				}
				if m {
					activated = append(activated, dir)
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
	}
	sort.Strings(activated)
	return activated
}
