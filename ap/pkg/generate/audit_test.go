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

package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

func TestCommentSyntax(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		data       []byte
		wantPrefix string
		wantSuffix string
		wantOK     bool
	}{
		{name: "go", path: "main.go", data: []byte("package main"), wantPrefix: "//", wantOK: true},
		{name: "proto", path: "service.proto", data: []byte("syntax = \"proto3\";"), wantPrefix: "//", wantOK: true},
		{name: "c", path: "main.c", data: []byte("int main() {}"), wantPrefix: "//", wantOK: true},
		{name: "rust", path: "lib.rs", data: []byte("fn main() {}"), wantPrefix: "//", wantOK: true},
		{name: "shell", path: "script.sh", data: []byte("#!/bin/bash"), wantPrefix: "#", wantOK: true},
		{name: "yaml", path: "config.yaml", data: []byte("key: value"), wantPrefix: "#", wantOK: true},
		{name: "toml", path: "config.toml", data: []byte("key = \"val\""), wantPrefix: "#", wantOK: true},
		{name: "markdown", path: "README.md", data: []byte("# Title"), wantPrefix: "<!--", wantSuffix: " -->", wantOK: true},
		{name: "html", path: "index.html", data: []byte("<html></html>"), wantPrefix: "<!--", wantSuffix: " -->", wantOK: true},
		{name: "sql", path: "schema.sql", data: []byte("CREATE TABLE;"), wantPrefix: "--", wantOK: true},
		{name: "dockerfile", path: "Dockerfile", data: []byte("FROM alpine"), wantPrefix: "#", wantOK: true},
		{name: "makefile", path: "Makefile", data: []byte("all:"), wantPrefix: "#", wantOK: true},
		{name: "gomod", path: "go.mod", data: []byte("module foo"), wantPrefix: "//", wantOK: true},
		{name: "json", path: "package.json", data: []byte("{\"name\": \"foo\"}"), wantOK: false},
		{name: "gosum", path: "go.sum", data: []byte("example.com/foo v1.0.0 h1:...="), wantOK: false},
		{name: "binary", path: "blob.bin", data: []byte{0x00, 0x01, 0x02}, wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prefix, suffix, ok := CommentSyntax(tc.path, tc.data)
			if ok != tc.wantOK {
				t.Fatalf("CommentSyntax(%q) ok = %v, want %v", tc.path, ok, tc.wantOK)
			}
			if ok {
				if prefix != tc.wantPrefix || suffix != tc.wantSuffix {
					t.Errorf("got (%q, %q), want (%q, %q)", prefix, suffix, tc.wantPrefix, tc.wantSuffix)
				}
			}
		})
	}
}

func TestRunAudit_DryRun(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create test files
	if err := os.WriteFile(filepath.Join(tempDir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "data.json"), []byte("{\"k\": \"v\"}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	r, err := sourcetree.Open(tempDir)
	if err != nil {
		t.Fatal(err)
	}
	scope := tasks.NewTaskContext(tempDir, tempDir, r)

	err = RunAudit(t.Context(), tempDir, []*tasks.TaskContext{scope}, AuditOptions{DryRun: true})
	if err != nil {
		t.Fatalf("RunAudit with DryRun failed: %v", err)
	}
}

func TestRunAudit_EndToEndFindings(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a normal source file
	sourceContent := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(tempDir, "main.go"), []byte(sourceContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a generated file that lacks a header (written by dev/tasks/generate-test)
	genContent := "# Some generated content\nkey: value\n"
	genPath := filepath.Join(tempDir, "generated_no_header.yaml")
	if err := os.WriteFile(genPath, []byte(genContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a script generator dev/tasks/generate-test
	tasksDir := filepath.Join(tempDir, "dev", "tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatal(err)
	}
	scriptContent := `#!/bin/sh
cat << 'EOF' > generated_no_header.yaml
# Some generated content
key: value
EOF
`
	scriptPath := filepath.Join(tasksDir, "generate-test")
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatal(err)
	}

	r, err := sourcetree.Open(tempDir)
	if err != nil {
		t.Fatal(err)
	}

	// Informational mode: should succeed (exit 0) and find generated_no_header.yaml
	scope := tasks.NewTaskContext(tempDir, tempDir, r)
	err = RunAudit(t.Context(), tempDir, []*tasks.TaskContext{scope}, AuditOptions{Strict: false})
	if err != nil {
		t.Fatalf("RunAudit (non-strict) failed: %v", err)
	}

	// Strict mode: should fail because finding exists
	errStrict := RunAudit(t.Context(), tempDir, []*tasks.TaskContext{scope}, AuditOptions{Strict: true})
	if errStrict == nil {
		t.Fatalf("expected RunAudit (strict) to fail on unheadered generated file, got nil")
	}
	if !strings.Contains(errStrict.Error(), "audit-generated failed") {
		t.Errorf("unexpected error message: %v", errStrict)
	}
}

func TestRunAudit_WithGeneratedHeader_NoFinding(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a normal source file
	sourceContent := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(tempDir, "main.go"), []byte(sourceContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a generated file WITH standard header
	genContent := "# Code generated by test-gen. DO NOT EDIT.\nkey: value\n"
	genPath := filepath.Join(tempDir, "generated_with_header.yaml")
	if err := os.WriteFile(genPath, []byte(genContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create generator script
	tasksDir := filepath.Join(tempDir, "dev", "tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatal(err)
	}
	scriptContent := `#!/bin/sh
cat << 'EOF' > generated_with_header.yaml
# Code generated by test-gen. DO NOT EDIT.
key: value
EOF
`
	scriptPath := filepath.Join(tasksDir, "generate-test")
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatal(err)
	}

	r, err := sourcetree.Open(tempDir)
	if err != nil {
		t.Fatal(err)
	}

	scope := tasks.NewTaskContext(tempDir, tempDir, r)
	// Strict mode should succeed because the generated file HAS a header!
	err = RunAudit(t.Context(), tempDir, []*tasks.TaskContext{scope}, AuditOptions{Strict: true})
	if err != nil {
		t.Fatalf("RunAudit (strict) failed unexpectedly: %v", err)
	}
}
