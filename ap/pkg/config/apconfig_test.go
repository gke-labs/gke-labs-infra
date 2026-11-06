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
generate:
  ignoreOrphans:
    - "pkg/sandbox/*.pb.go"
  auditPaths:
    - "pkg/**"
    - "dev/**"
  auditGenerated: true
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

	if cfg.Generate == nil {
		t.Fatalf("expected Generate config to be non-nil")
	}

	if len(cfg.Generate.IgnoreOrphans) != 1 || cfg.Generate.IgnoreOrphans[0] != "pkg/sandbox/*.pb.go" {
		t.Errorf("unexpected ignoreOrphans: %+v", cfg.Generate.IgnoreOrphans)
	}

	if !cfg.Generate.AuditGenerated {
		t.Errorf("expected AuditGenerated=true, got false")
	}

	if len(cfg.Generate.AuditPaths) != 2 || cfg.Generate.AuditPaths[0] != "pkg/**" || cfg.Generate.AuditPaths[1] != "dev/**" {
		t.Errorf("unexpected auditPaths: %+v", cfg.Generate.AuditPaths)
	}
}

func TestLoadAPConfig_UnknownField(t *testing.T) {
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
	if !strings.Contains(err.Error(), ".ap/ap.yaml") {
		t.Errorf("expected error to mention .ap/ap.yaml, got %v", err)
	}
	if !strings.Contains(err.Error(), `"unknownKey"`) {
		t.Errorf("expected error to name unknown key path \"unknownKey\", got: %v", err)
	}
}

func TestLoadAPConfig_ValidateVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		wantErr bool
	}{
		{name: "empty is valid", version: "", wantErr: false},
		{name: "!self is valid", version: "!self", wantErr: false},
		{name: "latest is valid", version: "latest", wantErr: false},
		{name: "semver v0.1.0 is valid", version: "v0.1.0", wantErr: false},
		{name: "semver v1.2.3 is valid", version: "v1.2.3", wantErr: false},
		{name: "pseudo-version is valid", version: "v0.0.0-20261024111447-4ab120e48572", wantErr: false},
		{name: "missing v prefix is invalid", version: "1.2.3", wantErr: true},
		{name: "arbitrary string is invalid", version: "invalid", wantErr: true},
		{name: "branch name is invalid", version: "main", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &APConfig{Version: tc.version}
			err := cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected Validate() to fail for version %q, got nil", tc.version)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected Validate() to succeed for version %q, got: %v", tc.version, err)
			}
		})
	}
}

func TestLoadAPConfig_ValidatePresubmits(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		wantErr  bool
	}{
		{name: "valid globs", patterns: []string{"docs/*", "README.md", "*.md", "pkg/**"}, wantErr: false},
		{name: "empty pattern is invalid", patterns: []string{""}, wantErr: true},
		{name: "shell metacharacters are invalid", patterns: []string{"docs/*;rm -rf"}, wantErr: true},
		{name: "spaces are invalid", patterns: []string{"docs / *"}, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &APConfig{
				Presubmits: &PresubmitConfig{
					SkipIfOnlyChanged: tc.patterns,
				},
			}
			err := cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected Validate() to fail for patterns %v, got nil", tc.patterns)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected Validate() to succeed for patterns %v, got: %v", tc.patterns, err)
			}
		})
	}
}

func TestLoadAPConfig_NonExistent(t *testing.T) {
	tempDir := t.TempDir()
	cfg, err := LoadAPConfig(tempDir)
	if err != nil {
		t.Fatalf("expected nil error for non-existent config, got: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil config for non-existent config, got: %+v", cfg)
	}
}
