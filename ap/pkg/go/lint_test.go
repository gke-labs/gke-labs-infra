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

package golang

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHasGoFiles(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "hasgofiles-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name     string
		setup    func(root string) error
		expected bool
	}{
		{
			name: "empty directory",
			setup: func(root string) error {
				return nil
			},
			expected: false,
		},
		{
			name: "direct go file",
			setup: func(root string) error {
				return os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0644)
			},
			expected: true,
		},
		{
			name: "go file in subdirectory",
			setup: func(root string) error {
				dir := filepath.Join(root, "pkg")
				if err := os.Mkdir(dir, 0755); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package pkg"), 0644)
			},
			expected: true,
		},
		{
			name: "go file only in submodule",
			setup: func(root string) error {
				dir := filepath.Join(root, "submod")
				if err := os.Mkdir(dir, 0755); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module submod"), 0644); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package foo"), 0644)
			},
			expected: false,
		},
		{
			name: "go file in root and in submodule",
			setup: func(root string) error {
				if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0644); err != nil {
					return err
				}
				dir := filepath.Join(root, "submod")
				if err := os.Mkdir(dir, 0755); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module submod"), 0644); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package foo"), 0644)
			},
			expected: true,
		},
		{
			name: "non-go files only",
			setup: func(root string) error {
				return os.WriteFile(filepath.Join(root, "README.md"), []byte("# My Project"), 0644)
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(tmpDir, tt.name)
			if err := os.Mkdir(root, 0755); err != nil {
				t.Fatalf("Failed to create test root: %v", err)
			}
			if err := tt.setup(root); err != nil {
				t.Fatalf("Failed to setup test: %v", err)
			}

			got, err := hasGoFiles(root)
			if err != nil {
				t.Errorf("hasGoFiles() error = %v", err)
				return
			}
			if got != tt.expected {
				t.Errorf("hasGoFiles() = %v, want %v", got, tt.expected)
			}
		})
	}
}
