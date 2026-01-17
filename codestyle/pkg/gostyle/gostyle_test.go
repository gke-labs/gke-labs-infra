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

package gostyle

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRun_NoConfig(t *testing.T) {
	// Create a temporary directory for the mock repo
	tmpDir, err := os.MkdirTemp("", "gostyle-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()

	// Run should succeed (do nothing) when no config exists
	err = Run(ctx, tmpDir, nil)
	if err != nil {
		t.Errorf("Expected success from Run (no config), got error: %v", err)
	}
}

func TestRun_WithConfig(t *testing.T) {
	// Create a temporary directory for the mock repo
	tmpDir, err := os.MkdirTemp("", "gostyle-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create .ap directory
	apDir := filepath.Join(tmpDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatalf("Failed to create .ap dir: %v", err)
	}

	// Create go.yaml
	configContent := `
gofmt:
  enabled: true
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write go.yaml: %v", err)
	}

	// Create a badly formatted go file
	badContent := "package main\nfunc main() {\nprintln(\"hello\")\n}\n"
	badFile := filepath.Join(tmpDir, "bad.go")
	if err := os.WriteFile(badFile, []byte(badContent), 0644); err != nil {
		t.Fatalf("Failed to write bad.go: %v", err)
	}

	ctx := context.Background()
	if err := Run(ctx, tmpDir, nil); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Check if formatted
	formattedContent, err := os.ReadFile(badFile)
	if err != nil {
		t.Fatalf("Failed to read bad.go: %v", err)
	}

	expected := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	if string(formattedContent) != expected {
		t.Errorf("File was not formatted correctly.\nGot:\n%s\nExpected:\n%s", string(formattedContent), expected)
	}
}
