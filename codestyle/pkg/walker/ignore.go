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

package walker

import (
	"path/filepath"
	"strings"
)

// IgnoreList matches paths against a list of patterns, similar to .gitignore.
type IgnoreList struct {
	patterns []string
}

// NewIgnoreList creates a new IgnoreList.
func NewIgnoreList(patterns []string) *IgnoreList {
	return &IgnoreList{patterns: patterns}
}

// ShouldIgnore returns true if the path should be ignored.
// path should be relative to the root of the walk.
func (l *IgnoreList) ShouldIgnore(path string, isDir bool) bool {
	for _, p := range l.patterns {
		if match(p, path, isDir) {
			return true
		}
	}
	return false
}

func match(pattern, path string, isDir bool) bool {
	originalPattern := pattern

	// 1. If pattern ends with /, it only matches directories.
	mustBeDir := strings.HasSuffix(pattern, "/")
	if mustBeDir {
		if !isDir {
			return false
		}
		pattern = strings.TrimSuffix(pattern, "/")
	}

	// 2. Split into segments for matching.
	// Note: We assume pattern uses / as separator. Path might use OS separator.
	// We normalize path to use / for matching logic simplicity.
	path = filepath.ToSlash(path)

	// 3. Handle ** prefix
	// "**/testdata" matches "testdata", "pkg/testdata"
	if strings.HasPrefix(originalPattern, "**/") {
		subPattern := strings.TrimPrefix(originalPattern, "**/")
		// The subPattern might also end in /, e.g. "**/testdata/"
		subPattern = strings.TrimSuffix(subPattern, "/")

		// If subPattern has no slashes, match basename
		if !strings.Contains(subPattern, "/") {
			name := filepath.Base(path)
			matched, _ := filepath.Match(subPattern, name)
			if matched {
				return true
			}
		}

		// If subPattern matches the end of path
		// e.g. path="pkg/testdata", subPattern="testdata"
		if path == subPattern || strings.HasSuffix(path, "/"+subPattern) {
			return true
		}
		return false
	}

	// 4. If original pattern contains /, it matches against the full relative path.
	if strings.Contains(originalPattern, "/") {
		matched, _ := filepath.Match(pattern, path)
		return matched
	}

	// 5. If pattern does not contain /, it matches against the basename.
	name := filepath.Base(path)
	matched, _ := filepath.Match(pattern, name)
	return matched
}
