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

func TestRun_GoVet(t *testing.T) {
	// Create a temporary directory for the mock repo
	tmpDir, err := os.MkdirTemp("", "gostyle-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Setup .codestyle/go.yaml
	configDir := filepath.Join(tmpDir, ".codestyle")
	if err := os.Mkdir(configDir, 0755); err != nil {
		t.Fatalf("Failed to create config dir: %v", err)
	}
	configFile := filepath.Join(configDir, "go.yaml")
	configContent := []byte(`
govet:
  enabled: true
`)
	if err := os.WriteFile(configFile, configContent, 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Setup go.mod
	goModFile := filepath.Join(tmpDir, "go.mod")
	goModContent := []byte(`module example.com/test
go 1.20
`)
	if err := os.WriteFile(goModFile, goModContent, 0644); err != nil {
		t.Fatalf("Failed to write go.mod: %v", err)
	}

	// Create a Go file with a vet error
	// Printf format %d has arg "str" of wrong type string
	badGoFile := filepath.Join(tmpDir, "main.go")
	badGoContent := []byte(`package main
import "fmt"
func main() {
	fmt.Printf("%d", "str")
}
`)
	if err := os.WriteFile(badGoFile, badGoContent, 0644); err != nil {
		t.Fatalf("Failed to write main.go: %v", err)
	}

	ctx := context.Background()

	// Run should fail because of go vet error
	err = Run(ctx, tmpDir, nil)
	if err == nil {
		t.Error("Expected error from Run due to go vet failure, got nil")
	}

	// Fix the Go file
	goodGoContent := []byte(`package main
import "fmt"
func main() {
	fmt.Printf("%s", "str")
}
`)
	if err := os.WriteFile(badGoFile, goodGoContent, 0644); err != nil {
		t.Fatalf("Failed to write main.go: %v", err)
	}

	// Run should succeed now
	err = Run(ctx, tmpDir, nil)
	if err != nil {
		t.Errorf("Expected success from Run, got error: %v", err)
	}
}

func TestRun_GoVet_Disabled(t *testing.T) {
	// Create a temporary directory for the mock repo
	tmpDir, err := os.MkdirTemp("", "gostyle-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Setup .codestyle/go.yaml with govet disabled
	configDir := filepath.Join(tmpDir, ".codestyle")
	if err := os.Mkdir(configDir, 0755); err != nil {
		t.Fatalf("Failed to create config dir: %v", err)
	}
	configFile := filepath.Join(configDir, "go.yaml")
	configContent := []byte(`
govet:
  enabled: false
`)
	if err := os.WriteFile(configFile, configContent, 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Setup go.mod
	goModFile := filepath.Join(tmpDir, "go.mod")
	goModContent := []byte(`module example.com/test
go 1.20
`)
	if err := os.WriteFile(goModFile, goModContent, 0644); err != nil {
		t.Fatalf("Failed to write go.mod: %v", err)
	}

	// Create a Go file with a vet error
	badGoFile := filepath.Join(tmpDir, "main.go")
	badGoContent := []byte(`package main
import "fmt"
func main() {
	fmt.Printf("%d", "str")
}
`)
	if err := os.WriteFile(badGoFile, badGoContent, 0644); err != nil {
		t.Fatalf("Failed to write main.go: %v", err)
	}

	ctx := context.Background()

	// Run should succeed because govet is disabled
	err = Run(ctx, tmpDir, nil)
	if err != nil {
		t.Errorf("Expected success from Run (disabled govet), got error: %v", err)
	}
}
