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
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
tidy:
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
	if cfg.IsTidyEnabled() != false {
		t.Errorf("expected tidy enabled to be false")
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
	if cfg.IsTidyEnabled() != true {
		t.Errorf("expected default tidy enabled to be true")
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
	toi := cfg.Linter("testOnlyImports")
	if !toi.IsEnabled() {
		t.Errorf("expected default testOnlyImports enabled to be true")
	}
	if !toi.IsError() {
		t.Errorf("expected default testOnlyImports error to be true (error mode)")
	}
	cw := cfg.Linter("closeWriter")
	if !cw.IsEnabled() {
		t.Errorf("expected default closeWriter enabled to be true")
	}
	if !cw.IsError() {
		t.Errorf("expected default closeWriter error to be true (error mode)")
	}
}

func TestCloseWriterConfig(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "ap-closewriter-config-test")
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
  closeWriter:
    mode: warn
    types:
      - "*bufio.Writer"
      - example.com/custom.MyWriter
    skipGenerated: true
    skipTests: true
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tempDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	cw := cfg.Linter("closeWriter")
	if !cw.IsEnabled() {
		t.Errorf("expected closeWriter to be enabled")
	}
	if cw.IsError() {
		t.Errorf("expected closeWriter not to be error mode (warn)")
	}
	if cw.GetMode() != "warn" {
		t.Errorf("expected mode warn, got %q", cw.GetMode())
	}
	if cw.SkipGeneratedOverride() == nil || *cw.SkipGeneratedOverride() != true {
		t.Errorf("expected skipGenerated to be true")
	}
	if cw.SkipTestsOverride() == nil || *cw.SkipTestsOverride() != true {
		t.Errorf("expected skipTests to be true")
	}
}

func TestConfig_InvalidCloseWriterMode(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "ap-invalid-cw-mode-test")
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
  closeWriter:
    mode: invalid
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	_, err = Load(tempDir)
	if err == nil {
		t.Fatalf("expected Load to fail on invalid closeWriter mode")
	}
	if !strings.Contains(err.Error(), "invalid closeWriter mode") {
		t.Errorf("expected error to mention 'invalid closeWriter mode', got: %v", err)
	}
}

func TestTestOnlyImportsConfig(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "ap-testonlyimports-config-test")
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
  testOnlyImports:
    mode: warn
    packages:
      - github.com/google/go-cmp
      - example.com/custom
    skipGenerated: true
    skipTests: true
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tempDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	toi := cfg.Linter("testOnlyImports")
	if !toi.IsEnabled() {
		t.Errorf("expected testOnlyImports to be enabled")
	}
	if toi.IsError() {
		t.Errorf("expected testOnlyImports not to be error mode (warn)")
	}
	if toi.GetMode() != "warn" {
		t.Errorf("expected mode warn, got %q", toi.GetMode())
	}
	if toi.SkipGeneratedOverride() == nil || *toi.SkipGeneratedOverride() != true {
		t.Errorf("expected skipGenerated to be true")
	}
	if toi.SkipTestsOverride() == nil || *toi.SkipTestsOverride() != true {
		t.Errorf("expected skipTests to be true")
	}
}

func TestConfig_Linter_UnknownPanics(t *testing.T) {
	var cfg Config
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for unknown linter")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, `unknown linter "tpyo"`) {
			t.Errorf("expected panic message to mention unknown linter \"tpyo\", got: %v", r)
		}
	}()
	cfg.Linter("tpyo")
}

func TestConfig_Linter_KnownDefaults(t *testing.T) {
	known := []string{
		"unused",
		"testcontext",
		"replaceEmptyInterfaceWithAny",
		"droppedErrors",
		"testOnlyImports",
		"closeWriter",
		"deferInLoop",
	}
	var cfg *Config
	for _, name := range known {
		lc := cfg.Linter(name)
		if !lc.IsEnabled() {
			t.Errorf("%s: expected default IsEnabled to be true", name)
		}
		if !lc.IsError() {
			t.Errorf("%s: expected default IsError to be true", name)
		}
		if lc.SkipGeneratedOverride() != nil {
			t.Errorf("%s: expected default SkipGeneratedOverride to be nil", name)
		}
		if lc.SkipTestsOverride() != nil {
			t.Errorf("%s: expected default SkipTestsOverride to be nil", name)
		}
	}
}

func TestConfig_InvalidLinterModes(t *testing.T) {
	linters := []string{
		"unused",
		"testcontext",
		"replaceEmptyInterfaceWithAny",
		"droppedErrors",
		"testOnlyImports",
		"closeWriter",
		"deferInLoop",
	}
	for _, linter := range linters {
		t.Run(linter, func(t *testing.T) {
			tempDir := t.TempDir()
			apDir := filepath.Join(tempDir, ".ap")
			if err := os.Mkdir(apDir, 0755); err != nil {
				t.Fatal(err)
			}
			yamlContent := fmt.Sprintf("lint:\n  %s:\n    mode: invalid\n", linter)
			if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(yamlContent), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(tempDir)
			if err == nil {
				t.Fatalf("expected Load to fail on invalid %s mode", linter)
			}
			want := fmt.Sprintf("invalid %s mode \"invalid\"", linter)
			if !strings.Contains(err.Error(), want) {
				t.Errorf("expected error to contain %q, got: %v", want, err)
			}
		})
	}
}

func TestConfig_InvalidTestOnlyImportsMode(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "ap-invalid-mode-test")
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
  testOnlyImports:
    mode: invalid
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	_, err = Load(tempDir)
	if err == nil {
		t.Fatalf("expected Load to fail on invalid testOnlyImports mode")
	}
	if !strings.Contains(err.Error(), "invalid testOnlyImports mode") {
		t.Errorf("expected error to mention 'invalid testOnlyImports mode', got: %v", err)
	}
}

func TestCommonLinterConfig_ModeIgnore(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	yamlContent := `
lint:
  unused:
    mode: ignore
  replaceEmptyInterfaceWithAny:
    mode: ignore
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tempDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Linter("unused").IsEnabled() {
		t.Errorf("expected unused to be disabled when mode is ignore")
	}
	if cfg.Linter("replaceEmptyInterfaceWithAny").IsEnabled() {
		t.Errorf("expected replaceEmptyInterfaceWithAny to be disabled when mode is ignore")
	}
}

func TestCommonLinterConfig_Baseline(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	yamlContent := `
lint:
  closeWriter:
    baseline: .ap/custom-closewriter.txt
  unused:
    baseline: .ap/custom-unused.txt
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tempDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	cw := cfg.Linter("closeWriter")
	if cw.GetBaseline() != ".ap/custom-closewriter.txt" {
		t.Errorf("expected closewriter baseline .ap/custom-closewriter.txt, got %q", cw.GetBaseline())
	}

	u := cfg.Linter("unused")
	if u.GetBaseline() != ".ap/custom-unused.txt" {
		t.Errorf("expected unused baseline .ap/custom-unused.txt, got %q", u.GetBaseline())
	}
}

func TestConfig_InvalidUnusedMode(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	yamlContent := `
lint:
  unused:
    mode: badmode
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(tempDir)
	if err == nil {
		t.Fatalf("expected Load to fail on invalid unused mode")
	}
	if !strings.Contains(err.Error(), "invalid unused mode") {
		t.Errorf("expected error to mention 'invalid unused mode', got: %v", err)
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
  goos:
    - linux
  droppedErrors:
    mode: error
    exclude:
      - fmt.Fprintln
    baseline: .ap/droppederrors-baseline.txt
    skipTests: false
    skipGenerated: true
    useDefaultExcludes: false
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
	if len(cfg.LintGOOS()) != 1 || cfg.LintGOOS()[0] != "linux" {
		t.Errorf("unexpected GOOS list: %v", cfg.LintGOOS())
	}
}

func TestLintGOOSConfig(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		want    []string
		wantErr bool
	}{
		{
			name: "default auto when omitted",
			yaml: "lint:\n  skipGenerated: true\n",
			want: nil,
		},
		{
			name: "slice of goos",
			yaml: "lint:\n  goos:\n    - linux\n    - darwin\n",
			want: []string{"linux", "darwin"},
		},
		{
			name:    "string goos rejected by strict loading",
			yaml:    "lint:\n  goos: auto\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			apDir := filepath.Join(dir, ".ap")
			if err := os.Mkdir(apDir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(tt.yaml), 0644); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(dir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			got := cfg.LintGOOS()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("LintGOOS() = %v, want %v", got, tt.want)
			}
		})
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

func TestImageTag(t *testing.T) {
	// When IMAGE_TAG is unset, defaults to "latest"
	t.Setenv("IMAGE_TAG", "")
	var cfg *Config
	if got := cfg.ImageTag(); got != "latest" {
		t.Errorf("cfg.ImageTag() = %q, want %q", got, "latest")
	}
	if got := ImageTag(); got != "latest" {
		t.Errorf("ImageTag() = %q, want %q", got, "latest")
	}

	// When IMAGE_TAG is set, returns the configured value
	t.Setenv("IMAGE_TAG", "v1.2.3")
	if got := cfg.ImageTag(); got != "v1.2.3" {
		t.Errorf("cfg.ImageTag() = %q, want %q", got, "v1.2.3")
	}
	if got := ImageTag(); got != "v1.2.3" {
		t.Errorf("ImageTag() = %q, want %q", got, "v1.2.3")
	}
}
