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

func TestLoadPluginsConfig(t *testing.T) {
	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	content := `
plugins:
  astro:
    image: ghcr.io/gke-labs/astro-builder@sha256:4b22
    network: false
  local:
    dockerfile: images/local/Dockerfile
    network: true
`
	if err := os.WriteFile(filepath.Join(apDir, "plugins.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPluginsConfig(tempDir)
	if err != nil {
		t.Fatalf("LoadPluginsConfig failed: %v", err)
	}
	if len(cfg.Plugins) != 2 || !cfg.Plugins["astro"].IsEnabled() || !cfg.Plugins["local"].Network {
		t.Errorf("unexpected loaded plugins: %+v", cfg.Plugins)
	}
}

func TestLoadPluginsConfig_StrictAndValidation(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "unknown key",
			yaml: `
plugins:
  astro:
    image: ghcr.io/gke-labs/astro-builder@sha256:4b22
    netwrok: false
`,
			wantErr: "netwrok",
		},
		{
			name: "missing image and dockerfile",
			yaml: `
plugins:
  astro:
    network: false
`,
			wantErr: "must specify either image or dockerfile",
		},
		{
			name: "unpinned image",
			yaml: `
plugins:
  astro:
    image: ghcr.io/gke-labs/astro-builder:latest
`,
			wantErr: "must be pinned by digest (@sha256:...)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			apDir := filepath.Join(tempDir, ".ap")
			if err := os.Mkdir(apDir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(apDir, "plugins.yaml"), []byte(tc.yaml), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadPluginsConfig(tempDir)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}
