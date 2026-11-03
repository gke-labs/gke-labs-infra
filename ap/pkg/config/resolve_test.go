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
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
)

func TestResolveTargets(t *testing.T) {
	tempDir := t.TempDir()

	// Create repo structure
	// repo/
	//   .git/
	//   .ap/
	//   foo/
	//     .ap/
	//     bar/
	//       .ap/
	//   baz/

	repoRoot := filepath.Join(tempDir, "repo")
	os.MkdirAll(filepath.Join(repoRoot, ".git"), 0755)
	os.MkdirAll(filepath.Join(repoRoot, ".ap"), 0755)
	os.MkdirAll(filepath.Join(repoRoot, "foo", ".ap"), 0755)
	os.MkdirAll(filepath.Join(repoRoot, "foo", "bar", ".ap"), 0755)
	os.MkdirAll(filepath.Join(repoRoot, "baz"), 0755)

	tests := []struct {
		name       string
		args       []string
		currentDir string
		expected   []string
	}{
		{
			name:       "no args (defaults to .)",
			args:       []string{},
			currentDir: repoRoot,
			expected:   []string{repoRoot},
		},
		{
			name:       "dot",
			args:       []string{"."},
			currentDir: filepath.Join(repoRoot, "foo"),
			expected:   []string{filepath.Join(repoRoot, "foo")},
		},
		{
			name:       "go syntax recursive from root",
			args:       []string{"./..."},
			currentDir: repoRoot,
			expected:   []string{repoRoot},
		},
		{
			name:       "go syntax recursive from subfolder",
			args:       []string{"./..."},
			currentDir: filepath.Join(repoRoot, "foo"),
			expected:   []string{filepath.Join(repoRoot, "foo")},
		},
		{
			name:       "bazel syntax recursive",
			args:       []string{"//..."},
			currentDir: filepath.Join(repoRoot, "foo", "bar"),
			expected:   []string{repoRoot},
		},
		{
			name:       "bazel syntax absolute",
			args:       []string{"//foo"},
			currentDir: repoRoot,
			expected: []string{
				filepath.Join(repoRoot, "foo"),
			},
		},
		{
			name:       "go syntax relative",
			args:       []string{"bar"},
			currentDir: filepath.Join(repoRoot, "foo"),
			expected: []string{
				filepath.Join(repoRoot, "foo", "bar"),
			},
		},
		{
			name:       "bazel syntax absolute recursive",
			args:       []string{"//foo/..."},
			currentDir: repoRoot,
			expected: []string{
				filepath.Join(repoRoot, "foo"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := sourcetree.Open(repoRoot)
			if err != nil {
				t.Fatalf("sourcetree.Open failed: %v", err)
			}
			got, err := ResolveTargets(r, repoRoot, tt.currentDir, tt.args)
			if err != nil {
				t.Fatalf("ResolveTargets failed: %v", err)
			}

			// we sort to compare
			gotMap := make(map[string]bool)
			for _, g := range got {
				gotMap[g] = true
			}
			expMap := make(map[string]bool)
			for _, e := range tt.expected {
				expMap[e] = true
			}

			if !reflect.DeepEqual(gotMap, expMap) {
				t.Errorf("ResolveTargets() = %v, want %v", got, tt.expected)
			}
		})
	}
}
