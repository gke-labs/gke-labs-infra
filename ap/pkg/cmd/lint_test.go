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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunLint_UnknownKeyFails(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmpDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	apDir := filepath.Join(tmpDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module example.com/test\n\ngo 1.27\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Misspelled key under lint.unused
	yamlContent := `
lint:
  unused:
    skipGenerted: false
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(tmpDir)

	opt := LintOptions{
		RootOptions: &RootOptions{
			RepoRoot: tmpDir,
			APRoots:  []string{tmpDir},
			DryRun:   true,
		},
	}
	err := RunLint(t.Context(), opt)
	if err == nil {
		t.Fatalf("expected RunLint to fail with unknown key error")
	}
	if !strings.Contains(err.Error(), `"lint.unused.skipGenerted"`) {
		t.Errorf("expected error to name key path \"lint.unused.skipGenerted\", got: %v", err)
	}
	if !strings.Contains(err.Error(), `"skipGenerated"`) {
		t.Errorf("expected error to suggest \"skipGenerated\", got: %v", err)
	}
}
