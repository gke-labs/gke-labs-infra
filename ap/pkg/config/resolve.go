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
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
)

// ResolveTargets resolves directory specifiers to a list of target directories.
// Supported syntaxes:
//
//	.     - Current directory
//	./... - Current directory
//	//... - Repository root
//	//foo - The directory at repoRoot/foo
func ResolveTargets(r *sourcetree.SourceTree, repoRoot, currentDir string, args []string) ([]string, error) {
	if len(args) == 0 {
		args = []string{"."}
	}

	var targets []string
	seen := make(map[string]bool)

	for _, arg := range args {
		var resolved string

		switch {
		case arg == "." || arg == "./...":
			if currentDir != "" {
				resolved = currentDir
			} else {
				resolved = repoRoot
			}
		case arg == "//...":
			resolved = repoRoot
		default:
			path := ResolveTargetPath(repoRoot, currentDir, arg)
			if strings.HasSuffix(path, string(filepath.Separator)+"...") {
				path = strings.TrimSuffix(path, string(filepath.Separator)+"...")
			}
			resolved = path
		}

		cleanPath := filepath.Clean(resolved)
		if !seen[cleanPath] {
			targets = append(targets, cleanPath)
			seen[cleanPath] = true
		}
	}

	return targets, nil
}

// ResolveTargetPath converts a target argument (such as "//foo", "./bar", "baz", or absolute path)
// into an absolute filesystem path, stripping any ":label" suffix.
func ResolveTargetPath(repoRoot, currentDir, arg string) string {
	if strings.HasPrefix(arg, "//") {
		pathPart := strings.TrimPrefix(arg, "//")
		if idx := strings.Index(pathPart, ":"); idx != -1 {
			pathPart = pathPart[:idx]
		}
		if pathPart == "" {
			pathPart = "."
		}
		return filepath.Clean(filepath.Join(repoRoot, pathPart))
	}
	if filepath.IsAbs(arg) {
		return filepath.Clean(arg)
	}
	pathPart := arg
	if idx := strings.Index(pathPart, ":"); idx != -1 {
		pathPart = pathPart[:idx]
	}
	base := currentDir
	if base == "" {
		base = repoRoot
	}
	return filepath.Clean(filepath.Join(base, pathPart))
}
