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

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateHostsConfig(t *testing.T) {
	tmpDir := t.TempDir()
	hostsPath := filepath.Join(tmpDir, "images.local", "hosts.toml")
	ip := "10.96.0.10"

	err := updateHostsConfig(hostsPath, ip)
	if err != nil {
		t.Fatalf("updateHostsConfig failed: %v", err)
	}

	content, err := os.ReadFile(hostsPath)
	if err != nil {
		t.Fatalf("failed to read hosts.toml: %v", err)
	}

	expected := `server = "http://10.96.0.10"

[host."http://10.96.0.10"]
  capabilities = ["pull", "resolve"]
`
	if string(content) != expected {
		t.Errorf("unexpected content:\ngot:\n%s\nwant:\n%s", string(content), expected)
	}

	// Update with same IP, should not change anything (just verify it doesn't fail)
	err = updateHostsConfig(hostsPath, ip)
	if err != nil {
		t.Fatalf("second updateHostsConfig failed: %v", err)
	}

	// Update with new IP
	newIP := "10.96.0.11"
	err = updateHostsConfig(hostsPath, newIP)
	if err != nil {
		t.Fatalf("third updateHostsConfig failed: %v", err)
	}

	content, err = os.ReadFile(hostsPath)
	if err != nil {
		t.Fatalf("failed to read hosts.toml after update: %v", err)
	}
	if !strings.Contains(string(content), `[host."http://10.96.0.11"]`) {
		t.Errorf("expected updated IP in content: %s", string(content))
	}
}

func TestUpdateContainerdConfig(t *testing.T) {
	t.Run("DefaultVersion3", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.toml")

		initialContent := `[plugins]
  [plugins."io.containerd.grpc.v1.cri"]
    sandbox_image = "registry.k8s.io/pause:3.9"
`
		if err := os.WriteFile(configPath, []byte(initialContent), 0644); err != nil {
			t.Fatalf("failed to write initial config: %v", err)
		}

		changed, err := updateContainerdConfig(configPath)
		if err != nil {
			t.Fatalf("updateContainerdConfig failed: %v", err)
		}
		if !changed {
			t.Errorf("expected changed=true, got false")
		}

		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("failed to read config.toml: %v", err)
		}

		if !strings.Contains(string(content), `config_path = "/etc/containerd/certs.d"`) {
			t.Errorf("expected config_path in content: %s", string(content))
		}
		if !strings.Contains(string(content), `[plugins."io.containerd.cri.v1.images".registry]`) {
			t.Errorf("expected plugin section in content: %s", string(content))
		}

		// Run again, should not change
		changed, err = updateContainerdConfig(configPath)
		if err != nil {
			t.Fatalf("second updateContainerdConfig failed: %v", err)
		}
		if changed {
			t.Errorf("expected changed=false on second run, got true")
		}
	})

	t.Run("Version2", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.toml")

		initialContent := `version = 2
[plugins]
  [plugins."io.containerd.grpc.v1.cri"]
    sandbox_image = "registry.k8s.io/pause:3.9"
`
		if err := os.WriteFile(configPath, []byte(initialContent), 0644); err != nil {
			t.Fatalf("failed to write initial config: %v", err)
		}

		changed, err := updateContainerdConfig(configPath)
		if err != nil {
			t.Fatalf("updateContainerdConfig failed: %v", err)
		}
		if !changed {
			t.Errorf("expected changed=true, got false")
		}

		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("failed to read config.toml: %v", err)
		}

		if !strings.Contains(string(content), `config_path = "/etc/containerd/certs.d"`) {
			t.Errorf("expected config_path in content: %s", string(content))
		}
		if !strings.Contains(string(content), `[plugins."io.containerd.grpc.v1.cri".registry]`) {
			t.Errorf("expected v2 plugin section in content: %s", string(content))
		}

		// Run again, should not change
		changed, err = updateContainerdConfig(configPath)
		if err != nil {
			t.Fatalf("second updateContainerdConfig failed: %v", err)
		}
		if changed {
			t.Errorf("expected changed=false on second run, got true")
		}
	})

	t.Run("Version3", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.toml")

		initialContent := `version = 3
[plugins]
  [plugins."io.containerd.cri.v1.images".registry]
`
		if err := os.WriteFile(configPath, []byte(initialContent), 0644); err != nil {
			t.Fatalf("failed to write initial config: %v", err)
		}

		changed, err := updateContainerdConfig(configPath)
		if err != nil {
			t.Fatalf("updateContainerdConfig failed: %v", err)
		}
		if !changed {
			t.Errorf("expected changed=true, got false")
		}

		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("failed to read config.toml: %v", err)
		}

		if !strings.Contains(string(content), `config_path = "/etc/containerd/certs.d"`) {
			t.Errorf("expected config_path in content: %s", string(content))
		}
		if !strings.Contains(string(content), `[plugins."io.containerd.cri.v1.images".registry]`) {
			t.Errorf("expected plugin section in content: %s", string(content))
		}

		// Run again, should not change
		changed, err = updateContainerdConfig(configPath)
		if err != nil {
			t.Fatalf("second updateContainerdConfig failed: %v", err)
		}
		if changed {
			t.Errorf("expected changed=false on second run, got true")
		}
	})

	t.Run("Version2WithExistingHeader", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.toml")

		initialContent := `version = 2
[plugins]
  [plugins."io.containerd.grpc.v1.cri"]
    sandbox_image = "registry.k8s.io/pause:3.9"
  [plugins."io.containerd.grpc.v1.cri".registry]
`
		if err := os.WriteFile(configPath, []byte(initialContent), 0644); err != nil {
			t.Fatalf("failed to write initial config: %v", err)
		}

		changed, err := updateContainerdConfig(configPath)
		if err != nil {
			t.Fatalf("updateContainerdConfig failed: %v", err)
		}
		if !changed {
			t.Errorf("expected changed=true, got false")
		}

		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("failed to read config.toml: %v", err)
		}

		// Ensure we inserted config_path and didn't duplicate the table header
		if !strings.Contains(string(content), `config_path = "/etc/containerd/certs.d"`) {
			t.Errorf("expected config_path in content: %s", string(content))
		}

		// The header should appear exactly once
		count := strings.Count(string(content), `[plugins."io.containerd.grpc.v1.cri".registry]`)
		if count != 1 {
			t.Errorf("expected registry header to appear exactly once, got %d times in: %s", count, string(content))
		}

		// Run again, should not change
		changed, err = updateContainerdConfig(configPath)
		if err != nil {
			t.Fatalf("second updateContainerdConfig failed: %v", err)
		}
		if changed {
			t.Errorf("expected changed=false on second run, got true")
		}
	})
}
