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

package protoc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig_NonExistent(t *testing.T) {
	tempDir := t.TempDir()

	cfg, err := LoadConfig(tempDir)
	if err != nil {
		t.Fatalf("expected no error for non-existent config, got: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil config for non-existent config, got: %v", cfg)
	}
}

func TestLoadConfig_Empty(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatalf("failed to create .ap directory: %v", err)
	}

	configFile := filepath.Join(apDir, "proto.yaml")
	if err := os.WriteFile(configFile, []byte(""), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := LoadConfig(tempDir)
	if err != nil {
		t.Fatalf("expected no error for empty config, got: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config for empty config")
	}

	if cfg.Protoc != DefaultProtocVersion {
		t.Errorf("expected protoc to be %q, got %q", DefaultProtocVersion, cfg.Protoc)
	}
	if cfg.Plugins != nil {
		t.Errorf("expected plugins to be nil, got %v", cfg.Plugins)
	}
}

func TestLoadConfig_Full(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatalf("failed to create .ap directory: %v", err)
	}

	yamlContent := `
protoc: "21.12"
plugins:
  protoc-gen-go: "1.36.12"
  protoc-gen-go-grpc: "v1.6.2"
`
	configFile := filepath.Join(apDir, "proto.yaml")
	if err := os.WriteFile(configFile, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := LoadConfig(tempDir)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	if cfg.Protoc != "21.12" {
		t.Errorf("expected protoc to be 21.12, got %q", cfg.Protoc)
	}
	if cfg.Plugins["protoc-gen-go"] != "v1.36.12" {
		t.Errorf("expected protoc-gen-go to be v1.36.12, got %q", cfg.Plugins["protoc-gen-go"])
	}
	if cfg.Plugins["protoc-gen-go-grpc"] != "v1.6.2" {
		t.Errorf("expected protoc-gen-go-grpc to be v1.6.2, got %q", cfg.Plugins["protoc-gen-go-grpc"])
	}
}

func TestLoadConfig_UnknownKey(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatalf("failed to create .ap directory: %v", err)
	}

	yamlContent := `
protoc: "21.12"
unknown_key: "value"
`
	configFile := filepath.Join(apDir, "proto.yaml")
	if err := os.WriteFile(configFile, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	_, err := LoadConfig(tempDir)
	if err == nil {
		t.Fatal("expected error for unknown key, got nil")
	}
	if !strings.Contains(err.Error(), ".ap/proto.yaml") {
		t.Errorf("expected error to mention .ap/proto.yaml, got: %v", err)
	}
	if !strings.Contains(err.Error(), `"unknown_key"`) {
		t.Errorf("expected error to name unknown key path \"unknown_key\", got: %v", err)
	}
}
