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

package fileutils

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUntrustedRoot_WriteFile(t *testing.T) {
	rootDir := t.TempDir()
	outsideDir := t.TempDir()

	symlinkPath := filepath.Join(rootDir, "outside_symlink")
	if err := os.Symlink(outsideDir, symlinkPath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	root, err := OpenUntrustedRoot(rootDir)
	if err != nil {
		t.Fatalf("OpenUntrustedRoot failed: %v", err)
	}
	defer root.Close()

	testCases := []struct {
		name        string
		path        string
		content     []byte
		permissions os.FileMode
		wantErr     string
	}{
		{
			name:        "nested relative path",
			path:        "nested/dir/hello.txt",
			content:     []byte("hello world"),
			permissions: 0644,
			wantErr:     "",
		},
		{
			name:        "escape parent ../x",
			path:        "../x",
			content:     []byte("escape"),
			permissions: 0644,
			wantErr:     "escapes",
		},
		{
			name:        "escape parent a/../../x",
			path:        "a/../../x",
			content:     []byte("escape"),
			permissions: 0644,
			wantErr:     "escapes",
		},
		{
			name:        "absolute path outside",
			path:        filepath.Join(outsideDir, "abs.txt"),
			content:     []byte("escape"),
			permissions: 0644,
			wantErr:     "escapes",
		},
		{
			name:        "dot git pre-commit hook",
			path:        ".git/hooks/pre-commit",
			content:     []byte("bad hook"),
			permissions: 0755,
			wantErr:     "is under .git",
		},
		{
			name:        "dot git uppercase hook",
			path:        ".GIT/hooks/pre-commit",
			content:     []byte("bad hook"),
			permissions: 0755,
			wantErr:     "is under .git",
		},
		{
			name:        "sub dot git config",
			path:        "sub/.git/config",
			content:     []byte("bad config"),
			permissions: 0644,
			wantErr:     "is under .git",
		},
		{
			name:        "sub dot git mixed case config",
			path:        "sub/.Git/config",
			content:     []byte("bad config"),
			permissions: 0644,
			wantErr:     "is under .git",
		},
		{
			name:        "symlink pointing outside",
			path:        "outside_symlink/file.txt",
			content:     []byte("bad write"),
			permissions: 0644,
			wantErr:     "escapes",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := root.WriteFile(tc.path, tc.content, tc.permissions)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				fullPath := filepath.Join(rootDir, tc.path)
				got, err := os.ReadFile(fullPath)
				if err != nil {
					t.Fatalf("failed to read written file: %v", err)
				}
				if !bytes.Equal(got, tc.content) {
					t.Errorf("content mismatch: got %q, want %q", got, tc.content)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.path) {
					t.Errorf("error %q does not name offending path %q", err.Error(), tc.path)
				}
			}
		})
	}

	entries, err := os.ReadDir(outsideDir)
	if err != nil {
		t.Fatalf("failed to read outsideDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected outsideDir to be empty, found %d entries", len(entries))
	}
}
