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
	"reflect"
	"strings"
	"testing"
)

func TestEffective_Merge(t *testing.T) {
	tests := []struct {
		name          string
		baseGoYAML    string
		files         map[string]string // relative path -> content
		targetRelPath string
		check         func(t *testing.T, cfg *Config)
	}{
		{
			name: "base only",
			baseGoYAML: `
gofmt:
  enabled: true
govet:
  enabled: true
govulncheck:
  enabled: true
skip:
  - "vendor/**"
`,
			files: map[string]string{
				"pkg/foo.go": "package pkg\n",
			},
			targetRelPath: "pkg",
			check: func(t *testing.T, cfg *Config) {
				if !cfg.IsGofmtEnabled() {
					t.Errorf("expected gofmt enabled")
				}
				if !cfg.IsGovetEnabled() {
					t.Errorf("expected govet enabled")
				}
				if !cfg.IsGovulncheckEnabled() {
					t.Errorf("expected govulncheck enabled")
				}
				if !reflect.DeepEqual(cfg.Skip, []string{"vendor/**"}) {
					t.Errorf("expected skip [vendor/**], got %v", cfg.Skip)
				}
			},
		},
		{
			name: "subtree overrides one key",
			baseGoYAML: `
gofmt:
  enabled: true
govet:
  enabled: true
govulncheck:
  enabled: true
skip:
  - "vendor/**"
`,
			files: map[string]string{
				"sub/.ap/go.yaml": `
govulncheck:
  enabled: false
`,
				"sub/pkg/foo.go": "package pkg\n",
			},
			targetRelPath: "sub/pkg",
			check: func(t *testing.T, cfg *Config) {
				if cfg.IsGovulncheckEnabled() {
					t.Errorf("expected govulncheck disabled")
				}
				// Absent keys inherit from base
				if !cfg.IsGofmtEnabled() {
					t.Errorf("expected gofmt enabled")
				}
				if !cfg.IsGovetEnabled() {
					t.Errorf("expected govet enabled")
				}
				if !reflect.DeepEqual(cfg.Skip, []string{"vendor/**"}) {
					t.Errorf("expected skip [vendor/**], got %v", cfg.Skip)
				}
			},
		},
		{
			name: "nested subtrees nearest wins",
			baseGoYAML: `
govulncheck:
  enabled: true
govet:
  enabled: true
gofmt:
  enabled: true
`,
			files: map[string]string{
				"sub1/.ap/go.yaml": `
govulncheck:
  enabled: false
govet:
  enabled: false
`,
				"sub1/sub2/.ap/go.yaml": `
govulncheck:
  enabled: true
`,
				"sub1/sub2/foo.go": "package sub2\n",
			},
			targetRelPath: "sub1/sub2",
			check: func(t *testing.T, cfg *Config) {
				// Nearest (sub1/sub2) wins for govulncheck
				if !cfg.IsGovulncheckEnabled() {
					t.Errorf("expected govulncheck enabled from nearest subtree")
				}
				// Inherited from sub1 (enclosing)
				if cfg.IsGovetEnabled() {
					t.Errorf("expected govet disabled from sub1")
				}
				// Inherited from base
				if !cfg.IsGofmtEnabled() {
					t.Errorf("expected gofmt enabled from base")
				}
			},
		},
		{
			name: "list replaces",
			baseGoYAML: `
skip:
  - "vendor/**"
  - "build/**"
`,
			files: map[string]string{
				"sub/.ap/go.yaml": `
skip:
  - "custom/**"
`,
				"sub/bar.go": "package sub\n",
			},
			targetRelPath: "sub",
			check: func(t *testing.T, cfg *Config) {
				want := []string{"custom/**"}
				if !reflect.DeepEqual(cfg.Skip, want) {
					t.Errorf("expected skip %v, got %v", want, cfg.Skip)
				}
			},
		},
		{
			name: "subtree ap.yaml with version is ignored",
			baseGoYAML: `
gofmt:
  enabled: true
govulncheck:
  enabled: true
`,
			files: map[string]string{
				"sub/.ap/ap.yaml": `
version: latest
`,
				"sub/.ap/go.yaml": `
govulncheck:
  enabled: false
`,
				"sub/pkg/foo.go": "package pkg\n",
			},
			targetRelPath: "sub/pkg",
			check: func(t *testing.T, cfg *Config) {
				if cfg.IsGovulncheckEnabled() {
					t.Errorf("expected govulncheck disabled")
				}
				if !cfg.IsGofmtEnabled() {
					t.Errorf("expected gofmt enabled")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repoRoot := t.TempDir()
			apDir := filepath.Join(repoRoot, ".ap")
			if err := os.MkdirAll(apDir, 0755); err != nil {
				t.Fatal(err)
			}
			if tc.baseGoYAML != "" {
				if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(tc.baseGoYAML), 0644); err != nil {
					t.Fatal(err)
				}
			}

			for relPath, content := range tc.files {
				fullPath := filepath.Join(repoRoot, relPath)
				if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			}

			targetDir := filepath.Join(repoRoot, tc.targetRelPath)
			cfg, err := Effective(repoRoot, targetDir)
			if err != nil {
				t.Fatalf("Effective failed: %v", err)
			}

			tc.check(t, cfg)
		})
	}
}

func TestEffective_UnknownKeyRejected(t *testing.T) {
	repoRoot := t.TempDir()
	apDir := filepath.Join(repoRoot, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte("govet:\n  enabled: true\n"), 0644); err != nil {
		t.Fatal(err)
	}

	subDir := filepath.Join(repoRoot, "sub")
	subApDir := filepath.Join(subDir, ".ap")
	if err := os.MkdirAll(subApDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subApDir, "go.yaml"), []byte("unknownKey: true\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Effective(repoRoot, subDir)
	if err == nil {
		t.Fatal("expected error parsing subtree go.yaml with unknown key")
	}
	if !strings.Contains(err.Error(), "unknown key \"unknownKey\"") {
		t.Errorf("expected error to mention unknown key, got: %v", err)
	}
	if !strings.Contains(err.Error(), filepath.Join(subDir, ".ap", "go.yaml")) {
		t.Errorf("expected error to mention file path, got: %v", err)
	}
}

func TestEffective_LintAndToolchainOverrides(t *testing.T) {
	repoRoot := t.TempDir()
	apDir := filepath.Join(repoRoot, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	baseYAML := `
toolchain: go1.27.1
lint:
  skipGenerated: false
  droppedErrors:
    mode: error
    baseline: .ap/droppederrors-baseline.txt
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(baseYAML), 0644); err != nil {
		t.Fatal(err)
	}

	subDir := filepath.Join(repoRoot, "sub")
	subApDir := filepath.Join(subDir, ".ap")
	if err := os.MkdirAll(subApDir, 0755); err != nil {
		t.Fatal(err)
	}
	subYAML := `
toolchain: go1.27.3
lint:
  droppedErrors:
    mode: warn
    baseline: .ap/local-baseline.txt
`
	if err := os.WriteFile(filepath.Join(subApDir, "go.yaml"), []byte(subYAML), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Effective(repoRoot, subDir)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Toolchain != "go1.27.3" {
		t.Errorf("expected toolchain go1.27.3, got %q", cfg.Toolchain)
	}
	if cfg.Linter("droppedErrors").GetMode() != "warn" {
		t.Errorf("expected droppedErrors mode warn, got %q", cfg.Linter("droppedErrors").GetMode())
	}
	if cfg.DroppedErrorsBaseline() != ".ap/local-baseline.txt" {
		t.Errorf("expected droppedErrors baseline .ap/local-baseline.txt, got %q", cfg.DroppedErrorsBaseline())
	}
	// Inherited lint.skipGenerated
	if cfg.IsLintSkipGenerated() != false {
		t.Errorf("expected IsLintSkipGenerated false")
	}
}

func TestEffective_ExplicitEmptyOverride(t *testing.T) {
	repoRoot := t.TempDir()
	apDir := filepath.Join(repoRoot, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	baseYAML := `
toolchain: go1.27.1
lint:
  unused:
    mode: error
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(baseYAML), 0644); err != nil {
		t.Fatal(err)
	}

	subDir := filepath.Join(repoRoot, "sub")
	subApDir := filepath.Join(subDir, ".ap")
	if err := os.MkdirAll(subApDir, 0755); err != nil {
		t.Fatal(err)
	}
	subYAML := `
toolchain: ""
lint:
  unused:
    mode: ""
`
	if err := os.WriteFile(filepath.Join(subApDir, "go.yaml"), []byte(subYAML), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Effective(repoRoot, subDir)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Toolchain != "" {
		t.Errorf("expected toolchain to be overridden to empty string, got %q", cfg.Toolchain)
	}
	if mode := cfg.Linter("unused").GetMode(); mode != "" {
		t.Errorf("expected unused mode to be overridden to empty string, got %q", mode)
	}
}

func TestDeepMerge_PointerSectionsMergePerKey(t *testing.T) {
	dst := map[string]any{
		"gofmt": map[string]any{
			"enabled": true,
			"future":  "default-val",
		},
	}
	src := map[string]any{
		"gofmt": map[string]any{
			"enabled": false,
		},
	}

	merged := deepMerge(dst, src)
	gofmtMap, ok := merged["gofmt"].(map[string]any)
	if !ok {
		t.Fatalf("expected gofmt to be map[string]any, got %T", merged["gofmt"])
	}
	if enabled, ok := gofmtMap["enabled"].(bool); !ok || enabled != false {
		t.Errorf("expected gofmt.enabled to be false, got %v", gofmtMap["enabled"])
	}
	if future, ok := gofmtMap["future"].(string); !ok || future != "default-val" {
		t.Errorf("expected gofmt.future to be preserved as 'default-val', got %v", gofmtMap["future"])
	}
}

func TestEffective_PointerSectionsMergePerKey(t *testing.T) {
	repoRoot := t.TempDir()
	apDir := filepath.Join(repoRoot, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	baseYAML := `
lint:
  skipGenerated: true
  skipTests: true
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(baseYAML), 0644); err != nil {
		t.Fatal(err)
	}

	subDir := filepath.Join(repoRoot, "sub")
	subApDir := filepath.Join(subDir, ".ap")
	if err := os.MkdirAll(subApDir, 0755); err != nil {
		t.Fatal(err)
	}
	subYAML := `
lint:
  skipGenerated: false
`
	if err := os.WriteFile(filepath.Join(subApDir, "go.yaml"), []byte(subYAML), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Effective(repoRoot, subDir)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.IsLintSkipGenerated() != false {
		t.Errorf("expected skipGenerated overridden to false")
	}
	if cfg.IsLintSkipTests() != true {
		t.Errorf("expected skipTests to be preserved as true from base")
	}
}
