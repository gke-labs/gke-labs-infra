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
	"testing"
)

func TestIgnoreList(t *testing.T) {
	grid := []struct {
		Patterns []string
		Path     string
		IsDir    bool
		Want     bool
	}{
		// Basic directory ignore
		{
			Patterns: []string{"third_party/"},
			Path:     "third_party",
			IsDir:    true,
			Want:     true,
		},
		{
			Patterns: []string{"third_party/"},
			Path:     "third_party",
			IsDir:    false, // It's a file, but pattern has trailing slash, so strictly it shouldn't match if it were a file check?
			// Gitignore says "If the pattern ends with a slash, it is removed for the purpose of the following description, but it would only find a match with a directory. In other words, foo/ will match a directory foo and paths underneath it, but will not match a regular file or a symbolic link foo"
			// So if Path is "third_party" and IsDir is false, it should NOT match.
			Want: false,
		},
		{
			Patterns: []string{"third_party/"},
			Path:     "src/third_party",
			IsDir:    true,
			Want:     false, // Anchored to root unless starts with **/ or has no slash?
			// "If the pattern does not contain a slash /, Git treats it as a shell glob pattern and checks for a match against the pathname relative to the location of the .gitignore file (relative to the toplevel of the work tree if not from a .gitignore file)."
			// "If the pattern contains a slash ... git treats it as a shell glob suitable for consumption by fnmatch(3) with the FNM_PATHNAME flag: wildcards in the pattern will not match a / in the pathname."
			// So "third_party/" contains a slash (trailing). So it matches "third_party" at root.
			// It does NOT match "src/third_party".
		},

		// Recursive directory ignore
		{
			Patterns: []string{"**/testdata/"},
			Path:     "testdata",
			IsDir:    true,
			Want:     true,
		},
		{
			Patterns: []string{"**/testdata/"},
			Path:     "pkg/testdata",
			IsDir:    true,
			Want:     true,
		},

		// File ignore
		{
			Patterns: []string{"*_generated.go"},
			Path:     "foo_generated.go",
			IsDir:    false,
			Want:     true,
		},
		{
			Patterns: []string{"*_generated.go"},
			Path:     "pkg/foo_generated.go",
			IsDir:    false,
			Want:     true, // No slash in pattern, so matches basename
		},

		// Simple directory
		{
			Patterns: []string{"vendor"},
			Path:     "vendor",
			IsDir:    true,
			Want:     true,
		},
		{
			Patterns: []string{"vendor"},
			Path:     "pkg/vendor",
			IsDir:    true,
			Want:     true, // No slash, matches basename
		},
	}

	for _, g := range grid {
		l := NewIgnoreList(g.Patterns)
		got := l.ShouldIgnore(g.Path, g.IsDir)
		if got != g.Want {
			t.Errorf("ShouldIgnore(%q, isDir=%v) with patterns %v = %v, want %v", g.Path, g.IsDir, g.Patterns, got, g.Want)
		}
	}
}
