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
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
)

func TestExpandPaths(t *testing.T) {
	tempDir := t.TempDir()
	repoRoot := filepath.Join(tempDir, "repo")

	// Create structure
	os.MkdirAll(filepath.Join(repoRoot, "pkg", "foo"), 0755)
	os.MkdirAll(filepath.Join(repoRoot, "pkg", "bar"), 0755)
	os.MkdirAll(filepath.Join(repoRoot, "ignored_dir"), 0755)

	filesToCreate := []string{
		"pkg/foo/a.go",
		"pkg/foo/b.go",
		"pkg/bar/c.go",
		"ignored_dir/d.go",
		"root.go",
	}

	for _, file := range filesToCreate {
		p := filepath.Join(repoRoot, file)
		if err := os.WriteFile(p, []byte(""), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
	}

	ignore, err := NewIgnoreList([]string{"ignored_dir/", "pkg/foo/b.go"})
	if err != nil {
		t.Fatalf("failed to create ignore list: %v", err)
	}

	tests := []struct {
		name     string
		paths    []string
		expected []string
	}{
		{
			name:  "empty paths defaults to repoRoot",
			paths: []string{},
			expected: []string{
				filepath.Join(repoRoot, "pkg/foo/a.go"),
				filepath.Join(repoRoot, "pkg/bar/c.go"),
				filepath.Join(repoRoot, "root.go"),
			},
		},
		{
			name:  "specific directory pkg/bar",
			paths: []string{filepath.Join(repoRoot, "pkg/bar")},
			expected: []string{
				filepath.Join(repoRoot, "pkg/bar/c.go"),
			},
		},
		{
			name:  "explicit single file",
			paths: []string{filepath.Join(repoRoot, "root.go")},
			expected: []string{
				filepath.Join(repoRoot, "root.go"),
			},
		},
		{
			name: "explicit file that is ignored should be filtered out",
			paths: []string{
				filepath.Join(repoRoot, "pkg/foo/b.go"),
				filepath.Join(repoRoot, "root.go"),
			},
			expected: []string{
				filepath.Join(repoRoot, "root.go"),
			},
		},
		{
			name: "mixed targets with duplicates",
			paths: []string{
				filepath.Join(repoRoot, "pkg/bar"),
				filepath.Join(repoRoot, "pkg/bar/c.go"),
				filepath.Join(repoRoot, "root.go"),
			},
			expected: []string{
				filepath.Join(repoRoot, "pkg/bar/c.go"),
				filepath.Join(repoRoot, "root.go"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := sourcetree.Open(t.Context(), repoRoot)
			if err != nil {
				t.Fatalf("sourcetree.Open failed: %v", err)
			}
			got, err := ExpandPaths(r, repoRoot, tt.paths, ignore)
			if err != nil {
				t.Fatalf("ExpandPaths failed: %v", err)
			}

			// Convert to map sets to ignore order
			gotSet := make(map[string]bool)
			for _, g := range got {
				gotSet[g] = true
			}
			expectedSet := make(map[string]bool)
			for _, e := range tt.expected {
				expectedSet[e] = true
			}

			if !reflect.DeepEqual(gotSet, expectedSet) {
				t.Errorf("ExpandPaths() = %v, want %v", got, tt.expected)
			}
		})
	}
}
