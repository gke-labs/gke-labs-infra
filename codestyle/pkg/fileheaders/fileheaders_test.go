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

package fileheaders

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun_Skip(t *testing.T) {
	tmpDir := t.TempDir()

	// Create config
	configDir := filepath.Join(tmpDir, ".ap")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(configDir, "file-headers.yaml")
	configContent := `
license: apache-2.0
copyrightHolder: Google LLC
skip:
- "*.yaml"
`
	if err := os.WriteFile(configFile, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a yaml file that should be skipped
	subDir := filepath.Join(tmpDir, "foo/bar")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	targetFile := filepath.Join(subDir, "file.yaml")
	fileContent := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test
`
	if err := os.WriteFile(targetFile, []byte(fileContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a go file that should get a header
	goFile := filepath.Join(subDir, "test.go")
	goContent := `package bar`
	if err := os.WriteFile(goFile, []byte(goContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Run fileheaders
	ctx := context.Background()
	if err := Run(ctx, tmpDir, nil); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Verify yaml file was NOT modified
	content, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != fileContent {
		t.Errorf("YAML file was modified but should have been skipped. Content:\n%s", string(content))
	}

	// Verify go file WAS modified
	content, err = os.ReadFile(goFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "Copyright") {
		t.Errorf("Go file was NOT modified but should have been. Content:\n%s", string(content))
	}
}

func TestRun_Skip_ExplicitFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create config
	configDir := filepath.Join(tmpDir, ".ap")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(configDir, "file-headers.yaml")
	configContent := `
license: apache-2.0
copyrightHolder: Google LLC
skip:
- "*.yaml"
`
	if err := os.WriteFile(configFile, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a yaml file that should be skipped
	subDir := filepath.Join(tmpDir, "foo/bar")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	targetFile := filepath.Join(subDir, "file.yaml")
	fileContent := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test
`
	if err := os.WriteFile(targetFile, []byte(fileContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Run fileheaders with explicit file
	ctx := context.Background()
	if err := Run(ctx, tmpDir, []string{targetFile}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Verify yaml file was NOT modified
	content, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != fileContent {
		t.Errorf("YAML file was modified but should have been skipped. Content:\n%s", string(content))
	}
}

func TestRun_KubernetesStyle(t *testing.T) {
	tmpDir := t.TempDir()

	// Create config
	configDir := filepath.Join(tmpDir, ".ap")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(configDir, "file-headers.yaml")
	configContent := `
license: apache-2.0
copyrightHolder: Google LLC
`
	if err := os.WriteFile(configFile, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a go file with K8s style header
	targetFile := filepath.Join(tmpDir, "k8s.go")
	fileContent := `/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main
`
	if err := os.WriteFile(targetFile, []byte(fileContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Run fileheaders
	ctx := context.Background()
	if err := Run(ctx, tmpDir, []string{targetFile}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Verify file was NOT modified
	content, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != fileContent {
		t.Errorf("File was modified but should have been skipped. Content:\n%s", string(content))
	}
}
