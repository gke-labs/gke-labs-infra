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

package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"sigs.k8s.io/yaml"
)

// Effective returns the effective configuration for dir, taking into account
// the base configuration at the repository root and any enclosing subtree .ap/ap.yaml
// overrides beneath it. Resolution is per directory and memoized on r.
func Effective(r *sourcetree.SourceTree, dir string) (*Config, error) {
	cleanDir := filepath.Clean(dir)
	// If path points to a file, resolve to its enclosing directory
	if r != nil && r.FileExists(cleanDir) {
		cleanDir = filepath.Dir(cleanDir)
	} else if fi, err := os.Stat(cleanDir); err == nil && !fi.IsDir() {
		cleanDir = filepath.Dir(cleanDir)
	}

	repoRoot := findRepoRoot(r, cleanDir)
	if !filepath.IsAbs(cleanDir) {
		cleanDir = filepath.Clean(filepath.Join(repoRoot, cleanDir))
	}

	return resolveEffective(r, repoRoot, cleanDir)
}

// ForDir returns the effective configuration for dir.
// It is an alias for Effective.
func ForDir(r *sourcetree.SourceTree, dir string) (*Config, error) {
	return Effective(r, dir)
}

func resolveEffective(r *sourcetree.SourceTree, repoRoot, cleanDir string) (*Config, error) {
	cacheKey := "effective-config:" + cleanDir
	if r != nil {
		if cached, ok := r.GetCached(cacheKey); ok {
			if cfg, ok := cached.(*Config); ok {
				return cfg, nil
			}
		}
	}

	// Base case: repository root or a path outside the repository root
	if cleanDir == repoRoot || !isSubpath(repoRoot, cleanDir) {
		baseCfg, err := Load(repoRoot)
		if err != nil {
			return nil, err
		}
		if r != nil {
			r.SetCached(cacheKey, baseCfg)
		}
		return baseCfg, nil
	}

	// Recursive step: resolve parent directory's effective configuration
	parentDir := filepath.Dir(cleanDir)
	parentCfg, err := resolveEffective(r, repoRoot, parentDir)
	if err != nil {
		return nil, err
	}

	subtreeFile := filepath.Join(cleanDir, ".ap", "ap.yaml")
	data, exists, err := readFileIfExists(r, subtreeFile)
	if err != nil {
		return nil, fmt.Errorf("error reading %s: %w", subtreeFile, err)
	}

	if !exists {
		if r != nil {
			r.SetCached(cacheKey, parentCfg)
		}
		return parentCfg, nil
	}

	// Subtree .ap/ap.yaml exists: strictly validate it first
	var check Config
	if err := UnmarshalStrict(data, &check); err != nil {
		return nil, fmt.Errorf("error parsing %s: %w", subtreeFile, err)
	}

	var subtreeRaw any
	if err := yaml.Unmarshal(data, &subtreeRaw); err != nil {
		return nil, fmt.Errorf("error parsing %s: %w", subtreeFile, err)
	}
	subtreeMap, _ := toMap(subtreeRaw)
	if len(subtreeMap) == 0 {
		if r != nil {
			r.SetCached(cacheKey, parentCfg)
		}
		return parentCfg, nil
	}

	parentBytes, err := json.Marshal(parentCfg)
	if err != nil {
		return nil, fmt.Errorf("error serializing parent config: %w", err)
	}
	var parentRaw any
	if err := json.Unmarshal(parentBytes, &parentRaw); err != nil {
		return nil, fmt.Errorf("error parsing parent config map: %w", err)
	}
	parentMap, _ := toMap(parentRaw)

	mergedMap := mergeMaps(parentMap, subtreeMap)

	mergedBytes, err := json.Marshal(mergedMap)
	if err != nil {
		return nil, fmt.Errorf("error serializing merged config: %w", err)
	}

	var effective Config
	if err := json.Unmarshal(mergedBytes, &effective); err != nil {
		return nil, fmt.Errorf("error unmarshaling merged config: %w", err)
	}

	if err := effective.Validate(); err != nil {
		return nil, err
	}

	if r != nil {
		r.SetCached(cacheKey, &effective)
	}
	return &effective, nil
}

func mergeMaps(base, override map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		baseVal, exists := out[k]
		if !exists {
			out[k] = v
			continue
		}
		baseMap, baseIsMap := toMap(baseVal)
		overrideMap, overrideIsMap := toMap(v)
		if baseIsMap && overrideIsMap {
			out[k] = mergeMaps(baseMap, overrideMap)
		} else {
			out[k] = v
		}
	}
	return out
}

func readFileIfExists(r *sourcetree.SourceTree, path string) ([]byte, bool, error) {
	if r != nil {
		if f := r.File(path); f != nil {
			b, err := os.ReadFile(f.Path)
			if err != nil {
				return nil, false, err
			}
			return b, true, nil
		}
	}
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if fi.IsDir() {
		return nil, false, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

func findRepoRoot(r *sourcetree.SourceTree, dir string) string {
	if r != nil {
		if root := r.RepoRoot(); root != "" {
			return root
		}
		if root := r.Root(); root != "" {
			return root
		}
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = dir
	}
	curr := absDir
	var apRoot string
	for {
		if _, err := os.Stat(filepath.Join(curr, ".git")); err == nil {
			return curr
		} else if !os.IsNotExist(err) {
			break
		}
		if fi, err := os.Stat(filepath.Join(curr, ".ap", "go.yaml")); err == nil && !fi.IsDir() {
			apRoot = curr
		} else if err != nil && !os.IsNotExist(err) {
			break
		}
		parent := filepath.Dir(curr)
		if parent == curr {
			break
		}
		curr = parent
	}
	if apRoot != "" {
		return apRoot
	}
	return absDir
}

func isSubpath(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
