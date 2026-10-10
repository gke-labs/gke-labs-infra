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

func TestLoad_UnknownTopLevelKey(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	yamlContent := `
unknownTopLevel: true
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(tempDir)
	if err == nil {
		t.Fatalf("expected Load to fail with unknown top-level key")
	}
	if !strings.Contains(err.Error(), ".ap/go.yaml") {
		t.Errorf("expected error to name .ap/go.yaml, got %v", err)
	}
	if !strings.Contains(err.Error(), `"unknownTopLevel"`) {
		t.Errorf("expected error to name unknown key path \"unknownTopLevel\", got %v", err)
	}
}

func TestLoad_UnknownNestedKeyUnderLintCheck(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	yamlContent := `
lint:
  unused:
    unknownNestedField: true
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(tempDir)
	if err == nil {
		t.Fatalf("expected Load to fail with unknown nested key under lint.unused")
	}
	if !strings.Contains(err.Error(), ".ap/go.yaml") {
		t.Errorf("expected error to name .ap/go.yaml, got %v", err)
	}
	if !strings.Contains(err.Error(), `"lint.unused.unknownNestedField"`) {
		t.Errorf("expected error to name key path \"lint.unused.unknownNestedField\", got %v", err)
	}
}

func TestLoad_TypoOfKnownKey(t *testing.T) {
	tests := []struct {
		name       string
		yaml       string
		wantKey    string
		wantDidYou string
	}{
		{
			name: "typo in top-level skip",
			yaml: `
skp:
  - "vendor/**"
`,
			wantKey:    `"skp"`,
			wantDidYou: `"skip"`,
		},
		{
			name: "typo in lint check name",
			yaml: `
lint:
  droppedErros:
    mode: error
`,
			wantKey:    `"lint.droppedErros"`,
			wantDidYou: `"droppedErrors"`,
		},
		{
			name: "typo in lint.unused skipGenerated",
			yaml: `
lint:
  unused:
    skipGenerted: false
`,
			wantKey:    `"lint.unused.skipGenerted"`,
			wantDidYou: `"skipGenerated"`,
		},
		{
			name: "typo in lint.droppedErrors baseline",
			yaml: `
lint:
  droppedErrors:
    basline: .ap/baseline.txt
`,
			wantKey:    `"lint.droppedErrors.basline"`,
			wantDidYou: `"baseline"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			apDir := filepath.Join(tempDir, ".ap")
			if err := os.Mkdir(apDir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(tc.yaml), 0644); err != nil {
				t.Fatal(err)
			}

			_, err := Load(tempDir)
			if err == nil {
				t.Fatalf("expected Load to fail on typo key")
			}
			if !strings.Contains(err.Error(), tc.wantKey) {
				t.Errorf("expected error to mention key %s, got: %v", tc.wantKey, err)
			}
			if !strings.Contains(err.Error(), tc.wantDidYou) {
				t.Errorf("expected error to suggest %s, got: %v", tc.wantDidYou, err)
			}
		})
	}
}

func TestLoad_EveryDocumentedKey(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	yamlContent := `
gofmt:
  enabled: true
govet:
  enabled: true
govulncheck:
  enabled: true
skip:
  - "vendor/**"
lint:
  skipGenerated: false
  skipTests: false
  unused:
    enabled: true
    skipGenerated: false
    skipTests: true
  testcontext:
    mode: error
    skipGenerated: true
    skipTests: true
  unusedparameters:
    mode: warn
  replaceEmptyInterfaceWithAny:
    enabled: true
    skipGenerated: true
    skipTests: false
  droppedErrors:
    mode: error
    exclude:
      - fmt.Println
    baseline: .ap/droppederrors-baseline.txt
    skipTests: true
    skipGenerated: false
    useDefaultExcludes: true
    goos:
      - linux
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tempDir)
	if err != nil {
		t.Fatalf("expected Load to succeed with every documented key, got error: %v", err)
	}

	if !cfg.IsGofmtEnabled() {
		t.Errorf("expected gofmt to be enabled")
	}
	if !cfg.IsGovetEnabled() {
		t.Errorf("expected govet to be enabled")
	}
	if !cfg.IsGovulncheckEnabled() {
		t.Errorf("expected govulncheck to be enabled")
	}
	if len(cfg.Skip) != 1 || cfg.Skip[0] != "vendor/**" {
		t.Errorf("expected skip [vendor/**], got %v", cfg.Skip)
	}
	if cfg.IsLintSkipGenerated() {
		t.Errorf("expected IsLintSkipGenerated to be false")
	}
	if cfg.IsLintSkipTests() {
		t.Errorf("expected IsLintSkipTests to be false")
	}

	// Verify per-check overrides
	if cfg.UnusedSkipGenerated() == nil || *cfg.UnusedSkipGenerated() != false {
		t.Errorf("expected UnusedSkipGenerated to be false")
	}
	if cfg.UnusedSkipTests() == nil || *cfg.UnusedSkipTests() != true {
		t.Errorf("expected UnusedSkipTests to be true")
	}
	if cfg.TestContextSkipGenerated() == nil || *cfg.TestContextSkipGenerated() != true {
		t.Errorf("expected TestContextSkipGenerated to be true")
	}
	if cfg.TestContextSkipTests() == nil || *cfg.TestContextSkipTests() != true {
		t.Errorf("expected TestContextSkipTests to be true")
	}
	if cfg.ReplaceEmptyInterfaceWithAnySkipGenerated() == nil || *cfg.ReplaceEmptyInterfaceWithAnySkipGenerated() != true {
		t.Errorf("expected ReplaceEmptyInterfaceWithAnySkipGenerated to be true")
	}
	if cfg.ReplaceEmptyInterfaceWithAnySkipTests() == nil || *cfg.ReplaceEmptyInterfaceWithAnySkipTests() != false {
		t.Errorf("expected ReplaceEmptyInterfaceWithAnySkipTests to be false")
	}
	if !cfg.DroppedErrorsSkipTests() {
		t.Errorf("expected DroppedErrorsSkipTests to be true")
	}
	if cfg.DroppedErrorsSkipGenerated() {
		t.Errorf("expected DroppedErrorsSkipGenerated to be false")
	}
	if cfg.DroppedErrorsBaseline() != ".ap/droppederrors-baseline.txt" {
		t.Errorf("expected baseline .ap/droppederrors-baseline.txt, got %q", cfg.DroppedErrorsBaseline())
	}
}

func TestLoad_BareBooleanForStructWithEnabled(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	yamlContent := `
gofmt: false
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(tempDir)
	if err == nil {
		t.Fatalf("expected Load to fail when bare boolean given for gofmt")
	}
	if !strings.Contains(err.Error(), "gofmt.enabled") {
		t.Errorf("expected error to suggest gofmt.enabled, got: %v", err)
	}
}

func TestLoadHeaders_UnknownKey(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	yamlContent := `
licnese: apache-2.0
copyrightHolder: Google LLC
`
	if err := os.WriteFile(filepath.Join(apDir, "headers.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadHeaders(tempDir)
	if err == nil {
		t.Fatalf("expected LoadHeaders to fail on unknown key")
	}
	if !strings.Contains(err.Error(), ".ap/headers.yaml") {
		t.Errorf("expected error to mention .ap/headers.yaml, got %v", err)
	}
	if !strings.Contains(err.Error(), `"licnese"`) {
		t.Errorf("expected error to name \"licnese\", got %v", err)
	}
	if !strings.Contains(err.Error(), `"license"`) {
		t.Errorf("expected error to suggest \"license\", got %v", err)
	}
}

func TestLoadImagesConfig_UnknownKey(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	yamlContent := `
platform:
  - linux/amd64
`
	if err := os.WriteFile(filepath.Join(apDir, "images.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadImagesConfig(tempDir)
	if err == nil {
		t.Fatalf("expected LoadImagesConfig to fail on unknown key")
	}
	if !strings.Contains(err.Error(), ".ap/images.yaml") {
		t.Errorf("expected error to mention .ap/images.yaml, got %v", err)
	}
	if !strings.Contains(err.Error(), `"platform"`) {
		t.Errorf("expected error to name \"platform\", got %v", err)
	}
	if !strings.Contains(err.Error(), `"platforms"`) {
		t.Errorf("expected error to suggest \"platforms\", got %v", err)
	}
}
