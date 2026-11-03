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

package cmd

import (
	"path/filepath"
	"testing"
)

func TestBuildFormatCommand(t *testing.T) {
	rootOpt := &RootOptions{}
	cmd := BuildFormatCommand(rootOpt)
	if cmd == nil {
		t.Fatal("BuildFormatCommand returned nil")
	}
	if cmd.Use != "format [path...]" {
		t.Errorf("Unexpected use string: %q", cmd.Use)
	}
}

func TestRunFormat_DryRun(t *testing.T) {
	tempDir := t.TempDir()
	repoRoot := filepath.Join(tempDir, "repo")

	opt := FormatOptions{
		RootOptions: &RootOptions{
			RepoRoot: repoRoot,
			Dir:      repoRoot,
			DryRun:   true,
		},
	}

	// Should not panic or fail with dry run enabled even if directory structure is mock
	err := RunFormat(t.Context(), opt)
	if err != nil {
		t.Fatalf("RunFormat dry run failed: %v", err)
	}
}
