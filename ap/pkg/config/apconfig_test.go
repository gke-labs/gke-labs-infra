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

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAPConfig(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	content := `
version: "!self"
generatedHeader: "v2"
generate:
  ignoreOrphans:
    - "pkg/sandbox/*.pb.go"
`
	if err := os.WriteFile(filepath.Join(apDir, "ap.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadAPConfig(tempDir)
	if err != nil {
		t.Fatalf("LoadAPConfig failed: %v", err)
	}
	if cfg == nil {
		t.Fatalf("expected non-nil config")
	}

	if cfg.Version != "!self" {
		t.Errorf("expected version '!self', got %q", cfg.Version)
	}

	if cfg.GeneratedHeader != "v2" {
		t.Errorf("expected generatedHeader 'v2', got %q", cfg.GeneratedHeader)
	}

	if cfg.Generate == nil {
		t.Fatalf("expected Generate config to be non-nil")
	}

	if len(cfg.Generate.IgnoreOrphans) != 1 || cfg.Generate.IgnoreOrphans[0] != "pkg/sandbox/*.pb.go" {
		t.Errorf("unexpected ignoreOrphans: %+v", cfg.Generate.IgnoreOrphans)
	}
}

func TestLoadAPConfig_UnknownKey(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	content := `
version: "!self"
unknownKey: "value"
`
	if err := os.WriteFile(filepath.Join(apDir, "ap.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadAPConfig(tempDir)
	if err == nil {
		t.Fatalf("expected error for unknown key, got nil")
	}
	if !strings.Contains(err.Error(), "unknown key") {
		t.Errorf("expected error to mention unknown key, got: %v", err)
	}
}
