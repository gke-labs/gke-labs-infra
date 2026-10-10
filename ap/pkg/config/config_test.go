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
	"testing"
)

func TestLoad(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "ap-config-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	apDir := filepath.Join(tempDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	yamlContent := `
gofmt:
  enabled: false
govet:
  enabled: true
govulncheck:
  enabled: false
skip:
  - vendor/
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tempDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.IsGofmtEnabled() != false {
		t.Errorf("expected gofmt enabled to be false")
	}
	if cfg.IsGovetEnabled() != true {
		t.Errorf("expected govet enabled to be true")
	}
	if cfg.IsGovulncheckEnabled() != false {
		t.Errorf("expected govulncheck enabled to be false")
	}
	if len(cfg.Skip) != 1 || cfg.Skip[0] != "vendor/" {
		t.Errorf("unexpected skip list: %v", cfg.Skip)
	}
}

func TestLoadDefault(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "ap-config-test-default")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	cfg, err := Load(tempDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.IsGofmtEnabled() != true {
		t.Errorf("expected default gofmt enabled to be true")
	}
	if cfg.IsGovetEnabled() != true {
		t.Errorf("expected default govet enabled to be true")
	}
	if cfg.IsGovulncheckEnabled() != true {
		t.Errorf("expected default govulncheck enabled to be true")
	}
	if cfg.IsDroppedErrorsEnabled() != true {
		t.Errorf("expected default droppederrors enabled to be true")
	}
	if cfg.IsDroppedErrorsError() != true {
		t.Errorf("expected default droppederrors error to be true (error mode)")
	}
	if cfg.DroppedErrorsMode() != "error" {
		t.Errorf("expected default droppederrors mode to be error, got %q", cfg.DroppedErrorsMode())
	}
	if cfg.DroppedErrorsSkipTests() != true {
		t.Errorf("expected default droppederrors skipTests to be true")
	}
	if cfg.DroppedErrorsSkipGenerated() != false {
		t.Errorf("expected default droppederrors skipGenerated to be false")
	}
	if cfg.DroppedErrorsUseDefaultExcludes() != true {
		t.Errorf("expected default droppederrors useDefaultExcludes to be true")
	}
}

func TestDroppedErrorsConfig(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "ap-droppederrors-config-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	apDir := filepath.Join(tempDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	yamlContent := `
lint:
  droppedErrors:
    mode: error
    exclude:
      - fmt.Fprintln
    baseline: .ap/droppederrors-baseline.txt
    skipTests: false
    skipGenerated: true
    useDefaultExcludes: false
    goos:
      - linux
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tempDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if !cfg.IsDroppedErrorsEnabled() {
		t.Errorf("expected droppederrors to be enabled")
	}
	if !cfg.IsDroppedErrorsError() {
		t.Errorf("expected droppederrors to be error mode")
	}
	if cfg.DroppedErrorsMode() != "error" {
		t.Errorf("expected mode error, got %q", cfg.DroppedErrorsMode())
	}
	if cfg.DroppedErrorsBaseline() != ".ap/droppederrors-baseline.txt" {
		t.Errorf("expected baseline .ap/droppederrors-baseline.txt, got %q", cfg.DroppedErrorsBaseline())
	}
	if len(cfg.DroppedErrorsExclude()) != 1 || cfg.DroppedErrorsExclude()[0] != "fmt.Fprintln" {
		t.Errorf("unexpected exclude list: %v", cfg.DroppedErrorsExclude())
	}
	if cfg.DroppedErrorsSkipTests() != false {
		t.Errorf("expected skipTests to be false")
	}
	if cfg.DroppedErrorsSkipGenerated() != true {
		t.Errorf("expected skipGenerated to be true")
	}
	if cfg.DroppedErrorsUseDefaultExcludes() != false {
		t.Errorf("expected useDefaultExcludes to be false")
	}
	if len(cfg.DroppedErrorsGOOS()) != 1 || cfg.DroppedErrorsGOOS()[0] != "linux" {
		t.Errorf("unexpected GOOS list: %v", cfg.DroppedErrorsGOOS())
	}
}

func TestConfig_InvalidDroppedErrorsMode(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	yamlContent := `
lint:
  droppedErrors:
    mode: eror
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(tempDir)
	if err == nil {
		t.Fatalf("expected Load to fail on invalid droppedErrors mode 'eror'")
	}
}

func TestLintConfig_SkipGenerated(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 1. Cross-linter skipGenerated: true with per-check override on droppedErrors
	yamlContent := `
lint:
  skipGenerated: true
  droppedErrors:
    skipGenerated: false
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tempDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if !cfg.IsLintSkipGenerated() {
		t.Errorf("expected IsLintSkipGenerated to be true")
	}
	if cfg.DroppedErrorsSkipGenerated() {
		t.Errorf("expected droppedErrors to override skipGenerated to false")
	}
	// For linters without overrides, ResolveSkipGenerated inherits lint.skipGenerated
	if !cfg.ResolveSkipGenerated(nil, false) {
		t.Errorf("expected ResolveSkipGenerated to inherit skipGenerated: true")
	}
}

func TestLintConfig_SkipTests(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 1. Default (no skipTests specified)
	cfgDefault, err := Load(tempDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfgDefault.IsLintSkipTests() {
		t.Errorf("expected default IsLintSkipTests to be false")
	}
	if cfgDefault.ResolveSkipTests(nil, false) {
		t.Errorf("expected default ResolveSkipTests to be false when default is false")
	}
	if !cfgDefault.DroppedErrorsSkipTests() {
		t.Errorf("expected default DroppedErrorsSkipTests to be true")
	}

	// 2. lint.skipTests: true with droppedErrors override to false
	yamlContent := `
lint:
  skipTests: true
  droppedErrors:
    skipTests: false
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tempDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if !cfg.IsLintSkipTests() {
		t.Errorf("expected IsLintSkipTests to be true")
	}
	if !cfg.ResolveSkipTests(nil, false) {
		t.Errorf("expected ResolveSkipTests to inherit skipTests: true")
	}
	if cfg.DroppedErrorsSkipTests() {
		t.Errorf("expected droppedErrors to override skipTests to false")
	}
}

func TestLoadImagesConfig(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "ap-imagesconfig-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	apDir := filepath.Join(tempDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 1. Test defaults with empty file
	if err := os.WriteFile(filepath.Join(apDir, "images.yaml"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadImagesConfig(tempDir)
	if err != nil {
		t.Fatalf("LoadImagesConfig failed: %v", err)
	}
	platforms := cfg.GetPlatforms()
	if len(platforms) != 2 || platforms[0] != "linux/amd64" || platforms[1] != "linux/arm64" {
		t.Errorf("expected default platforms [linux/amd64, linux/arm64], got %v", platforms)
	}

	// 2. Test explicit config with short names
	yamlContent := `
platforms:
  - amd64
  - arm64
  - linux/s390x
`
	if err := os.WriteFile(filepath.Join(apDir, "images.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadImagesConfig(tempDir)
	if err != nil {
		t.Fatalf("LoadImagesConfig failed: %v", err)
	}
	platforms = cfg.GetPlatforms()
	expected := []string{"linux/amd64", "linux/arm64", "linux/s390x"}
	if len(platforms) != len(expected) {
		t.Fatalf("expected platforms count %d, got %d (platforms: %v)", len(expected), len(platforms), platforms)
	}
	for i, p := range platforms {
		if p != expected[i] {
			t.Errorf("at index %d: expected %s, got %s", i, expected[i], p)
		}
	}
}
