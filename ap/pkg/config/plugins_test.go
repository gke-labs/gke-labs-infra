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

func TestLoadPluginsConfig_UnknownKey(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	content := `
plugins:
  astro:
    image: ghcr.io/gke-labs/astro-builder@sha256:4b22
    netwrok: false
`
	if err := os.WriteFile(filepath.Join(apDir, "plugins.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadPluginsConfig(tempDir)
	if err == nil {
		t.Fatalf("expected LoadPluginsConfig to fail on unknown key")
	}
	if !strings.Contains(err.Error(), "netwrok") {
		t.Errorf("expected error to name unknown key netwrok, got %v", err)
	}
}

func TestLoadPluginsConfig_UnpinnedImageRejected(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	content := `
plugins:
  astro:
    image: ghcr.io/gke-labs/astro-builder:latest
`
	if err := os.WriteFile(filepath.Join(apDir, "plugins.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadPluginsConfig(tempDir)
	if err == nil {
		t.Fatalf("expected LoadPluginsConfig to fail on unpinned image")
	}
	if !strings.Contains(err.Error(), "must be pinned by digest (@sha256:...)") {
		t.Errorf("expected error to require digest pinning, got %v", err)
	}
}

func TestLoadPluginsConfig_Validation(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		wantErrPart string
	}{
		{
			name:        "both image and dockerfile",
			yaml:        "plugins:\n  testplugin:\n    image: ghcr.io/mock@sha256:1234\n    dockerfile: Dockerfile\n",
			wantErrPart: "cannot specify both image and dockerfile",
		},
		{
			name:        "neither image nor dockerfile",
			yaml:        "plugins:\n  testplugin:\n    network: false\n",
			wantErrPart: "must specify either image or dockerfile",
		},
		{
			name: "valid dockerfile",
			yaml: "plugins:\n  testplugin:\n    dockerfile: Dockerfile\n",
		},
		{
			name: "valid git url dockerfile",
			yaml: "plugins:\n  testplugin:\n    dockerfile: https://github.com/gke-labs/gke-labs-infra/blob/main/ap/images/ap-astro/Dockerfile\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			apDir := filepath.Join(tempDir, ".ap")
			_ = os.Mkdir(apDir, 0755)
			_ = os.WriteFile(filepath.Join(apDir, "plugins.yaml"), []byte(tc.yaml), 0644)
			cfg, err := LoadPluginsConfig(tempDir)
			if tc.wantErrPart != "" {
				if err == nil || !strings.Contains(err.Error(), "testplugin") || !strings.Contains(err.Error(), tc.wantErrPart) {
					t.Fatalf("expected error naming plugin and containing %q, got: %v", tc.wantErrPart, err)
				}
			} else if err != nil || cfg.Plugins["testplugin"].Dockerfile == "" {
				t.Fatalf("unexpected error or empty dockerfile: %v", err)
			}
		})
	}
}
