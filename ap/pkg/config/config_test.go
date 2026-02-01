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
}
