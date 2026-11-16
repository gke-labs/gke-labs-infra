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
