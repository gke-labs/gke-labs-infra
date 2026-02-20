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

package k8s

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestFindManifests(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ap-deploy-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name     string
		files    []string
		expected []string
	}{
		{
			name: "legacy manifest.yaml",
			files: []string{
				"k8s/manifest.yaml",
				"not-k8s/manifest.yaml",
			},
			expected: []string{
				"k8s/manifest.yaml",
			},
		},
		{
			name: "recursive k8s",
			files: []string{
				"k8s/manifest.yaml",
				"k8s/sub/resource.yaml",
				"k8s/crds/crd.yml",
			},
			expected: []string{
				"k8s/crds/crd.yml",
				"k8s/manifest.yaml",
				"k8s/sub/resource.yaml",
			},
		},
		{
			name: "mixed files",
			files: []string{
				"k8s/resource.yaml",
				"k8s/readme.md",
				"k8s/script.sh",
			},
			expected: []string{
				"k8s/resource.yaml",
			},
		},
		{
			name: "nested k8s directory",
			files: []string{
				"pkg/something/k8s/resource.yaml",
			},
			expected: []string{
				"pkg/something/k8s/resource.yaml",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(tmpDir, tt.name)
			if err := os.MkdirAll(root, 0755); err != nil {
				t.Fatal(err)
			}

			for _, f := range tt.files {
				path := filepath.Join(root, f)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("content"), 0644); err != nil {
					t.Fatal(err)
				}
			}

			got, err := findManifests(root)
			if err != nil {
				t.Fatalf("findManifests() error = %v", err)
			}

			var gotRel []string
			for _, g := range got {
				rel, err := filepath.Rel(root, g)
				if err != nil {
					t.Fatal(err)
				}
				gotRel = append(gotRel, rel)
			}
			sort.Strings(gotRel)
			sort.Strings(tt.expected)

			if len(gotRel) != len(tt.expected) {
				t.Errorf("got %v, want %v", gotRel, tt.expected)
				return
			}
			for i := range gotRel {
				if gotRel[i] != tt.expected[i] {
					t.Errorf("at index %d: got %s, want %s", i, gotRel[i], tt.expected[i])
				}
			}
		})
	}
}
