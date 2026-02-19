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

package images

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHasImages(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ap-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name     string
		setup    func(root string)
		expected bool
	}{
		{
			name: "no images",
			setup: func(root string) {
				os.MkdirAll(filepath.Join(root, "pkg"), 0755)
			},
			expected: false,
		},
		{
			name: "valid image",
			setup: func(root string) {
				os.MkdirAll(filepath.Join(root, "images", "foo"), 0755)
				os.WriteFile(filepath.Join(root, "images", "foo", "Dockerfile"), []byte("FROM scratch"), 0644)
			},
			expected: true,
		},
		{
			name: "nested image",
			setup: func(root string) {
				os.MkdirAll(filepath.Join(root, "pkg", "images", "foo"), 0755)
				os.WriteFile(filepath.Join(root, "pkg", "images", "foo", "Dockerfile"), []byte("FROM scratch"), 0644)
			},
			expected: true,
		},
		{
			name: "invalid structure",
			setup: func(root string) {
				os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch"), 0644)
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(tmpDir, tt.name)
			os.MkdirAll(root, 0755)
			tt.setup(root)

			got, err := HasImages(root)
			if err != nil {
				t.Fatalf("HasImages() error = %v", err)
			}
			if got != tt.expected {
				t.Errorf("HasImages() = %v, want %v", got, tt.expected)
			}
		})
	}
}
