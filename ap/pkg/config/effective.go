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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"
)

// Effective returns the effective configuration for dir, taking into account
// the base configuration at repoRoot and any enclosing subtree .ap/go.yaml
// overrides walking from dir up to repoRoot.
func Effective(repoRoot, dir string) (*Config, error) {
	cleanRepoRoot := filepath.Clean(repoRoot)
	cleanDir := filepath.Clean(dir)
	if !filepath.IsAbs(cleanDir) {
		cleanDir = filepath.Clean(filepath.Join(cleanRepoRoot, cleanDir))
	}

	// Collect enclosing subtree .ap/go.yaml files from dir up to repoRoot
	var subtreeFiles []string
	for curr := cleanDir; curr != cleanRepoRoot && strings.HasPrefix(curr, cleanRepoRoot); curr = filepath.Dir(curr) {
		goFile := filepath.Join(curr, ".ap", "go.yaml")
		if fi, err := os.Stat(goFile); err == nil && !fi.IsDir() {
			subtreeFiles = append(subtreeFiles, goFile)
		} else if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("error checking %s: %w", goFile, err)
		}
	}

	// Reverse to apply top-down (outermost ancestor first, nearest last)
	for i, j := 0, len(subtreeFiles)-1; i < j; i, j = i+1, j-1 {
		subtreeFiles[i], subtreeFiles[j] = subtreeFiles[j], subtreeFiles[i]
	}

	var files []string
	rootFile := filepath.Join(cleanRepoRoot, ".ap", "go.yaml")
	if fi, err := os.Stat(rootFile); err == nil && !fi.IsDir() {
		files = append(files, rootFile)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("error checking %s: %w", rootFile, err)
	}
	files = append(files, subtreeFiles...)

	var mergedMap map[string]any
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("error reading %s: %w", f, err)
		}
		var check Config
		if err := UnmarshalStrict(data, &check); err != nil {
			return nil, fmt.Errorf("error parsing %s: %w", f, err)
		}
		var fileMap map[string]any
		if err := yaml.Unmarshal(data, &fileMap); err != nil {
			return nil, fmt.Errorf("error parsing %s: %w", f, err)
		}
		if fileMap != nil {
			mergedMap = deepMerge(mergedMap, fileMap)
		}
	}

	if mergedMap == nil {
		mergedMap = make(map[string]any)
	}

	mergedYAML, err := yaml.Marshal(mergedMap)
	if err != nil {
		return nil, fmt.Errorf("error marshaling merged config: %w", err)
	}

	var effective Config
	if err := UnmarshalStrict(mergedYAML, &effective); err != nil {
		return nil, err
	}

	if err := effective.Validate(); err != nil {
		return nil, err
	}

	return &effective, nil
}

func deepMerge(dst, src map[string]any) map[string]any {
	if dst == nil {
		dst = make(map[string]any, len(src))
	}
	for k, vSrc := range src {
		if vSrcMap, ok := toMap(vSrc); ok {
			if vDstMap, ok := toMap(dst[k]); ok {
				dst[k] = deepMerge(vDstMap, vSrcMap)
			} else {
				dst[k] = deepCopyMap(vSrcMap)
			}
		} else {
			dst[k] = vSrc
		}
	}
	return dst
}

func deepCopyMap(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for k, v := range src {
		if subMap, ok := toMap(v); ok {
			out[k] = deepCopyMap(subMap)
		} else {
			out[k] = v
		}
	}
	return out
}
