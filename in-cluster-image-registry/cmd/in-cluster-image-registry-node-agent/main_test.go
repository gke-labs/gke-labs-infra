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
}

func TestUpdateHostsFile(t *testing.T) {
	tmpDir := t.TempDir()
	hostsPath := filepath.Join(tmpDir, "hosts")
	registryHost := "images.local"

	// 1. File does not exist: should create file
	ip1 := "10.96.0.10"
	err := updateHostsFile(hostsPath, ip1, registryHost)
	if err != nil {
		t.Fatalf("updateHostsFile failed for non-existent file: %v", err)
	}

	content, err := os.ReadFile(hostsPath)
	if err != nil {
		t.Fatalf("failed to read hosts file: %v", err)
	}
	expected1 := "10.96.0.10 images.local\n"
	if string(content) != expected1 {
		t.Errorf("expected:\n%q\ngot:\n%q", expected1, string(content))
	}

	// 2. Already correct: should not touch file / modify content
	// We check modtime/stat or just verify it does not error or alter the content
	err = updateHostsFile(hostsPath, ip1, registryHost)
	if err != nil {
		t.Fatalf("updateHostsFile failed when already correct: %v", err)
	}
	content, err = os.ReadFile(hostsPath)
	if err != nil {
		t.Fatalf("failed to read hosts file: %v", err)
	}
	if string(content) != expected1 {
		t.Errorf("content changed when already correct: got %q", string(content))
	}

	// 3. Different IP: should replace the existing line
	ip2 := "10.96.0.20"
	err = updateHostsFile(hostsPath, ip2, registryHost)
	if err != nil {
		t.Fatalf("updateHostsFile failed for updated IP: %v", err)
	}
	content, err = os.ReadFile(hostsPath)
	if err != nil {
		t.Fatalf("failed to read hosts file: %v", err)
	}
	expected2 := "10.96.0.20 images.local\n"
	if string(content) != expected2 {
		t.Errorf("expected updated IP:\n%q\ngot:\n%q", expected2, string(content))
	}

	// 4. File with unrelated entries and comments: should preserve them and replace images.local
	initialWithUnrelated := `127.0.0.1 localhost
::1 localhost

# Some custom comment
10.96.0.20 images.local
1.2.3.4 other.local
`
	if err := os.WriteFile(hostsPath, []byte(initialWithUnrelated), 0644); err != nil {
		t.Fatalf("failed to write initial with unrelated: %v", err)
	}

	ip3 := "10.96.0.30"
	err = updateHostsFile(hostsPath, ip3, registryHost)
	if err != nil {
		t.Fatalf("updateHostsFile failed: %v", err)
	}

	content, err = os.ReadFile(hostsPath)
	if err != nil {
		t.Fatalf("failed to read hosts file: %v", err)
	}

	expected3 := `127.0.0.1 localhost
::1 localhost

# Some custom comment
1.2.3.4 other.local
10.96.0.30 images.local
`
	if string(content) != expected3 {
		t.Errorf("expected:\n%q\ngot:\n%q", expected3, string(content))
	}
}
