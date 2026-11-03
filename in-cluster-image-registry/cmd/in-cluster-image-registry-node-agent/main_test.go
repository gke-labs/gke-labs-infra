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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/pelletier/go-toml/v2"
)

const fixtureA = `version = 2
required_plugins = ["io.containerd.grpc.v1.cri"]
# Kubernetes doesn't use containerd restart manager.
disabled_plugins = ["io.containerd.internal.v1.restart"]
oom_score = -999

[debug]
  level = "info"

[grpc]
  gid = 412

[plugins."io.containerd.grpc.v1.cri"]
  enable_cdi = true
  stream_server_address = "127.0.0.1"
  max_container_log_line_size = 262144
  sandbox_image = "gke.gcr.io/pause:3.8@sha256:880e63f94b145e46f1b1082bb71b85e21f16b99b180b9996407d61240ceb9830"
  image_pull_progress_timeout = "5m"
[plugins."io.containerd.grpc.v1.cri".cni]
  bin_dir = "/home/kubernetes/bin"
  conf_dir = "/etc/cni/net.d"
  conf_template = ""
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc.options]
  SystemdCgroup = true
[plugins."io.containerd.grpc.v1.cri".registry.mirrors."docker.io"]
  endpoint = ["https://mirror.gcr.io","https://registry-1.docker.io"]
[metrics]
 address = "127.0.0.1:1338"
[plugins."io.containerd.grpc.v1.cri".containerd]
  default_runtime_name = "runc"
  discard_unpacked_layers = true
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc]
  runtime_type = "io.containerd.runc.v2"

[plugins."io.containerd.cri.v1.images".registry]
  config_path = "/etc/containerd/certs.d"
`

const expectedOutputA = `version = 2
required_plugins = ["io.containerd.grpc.v1.cri"]
# Kubernetes doesn't use containerd restart manager.
disabled_plugins = ["io.containerd.internal.v1.restart"]
oom_score = -999

[debug]
  level = "info"

[grpc]
  gid = 412

[plugins."io.containerd.grpc.v1.cri"]
  enable_cdi = true
  stream_server_address = "127.0.0.1"
  max_container_log_line_size = 262144
  sandbox_image = "gke.gcr.io/pause:3.8@sha256:880e63f94b145e46f1b1082bb71b85e21f16b99b180b9996407d61240ceb9830"
  image_pull_progress_timeout = "5m"
[plugins."io.containerd.grpc.v1.cri".cni]
  bin_dir = "/home/kubernetes/bin"
  conf_dir = "/etc/cni/net.d"
  conf_template = ""
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc.options]
  SystemdCgroup = true
# [plugins."io.containerd.grpc.v1.cri".registry.mirrors."docker.io"]
#   endpoint = ["https://mirror.gcr.io","https://registry-1.docker.io"]
[metrics]
 address = "127.0.0.1:1338"
[plugins."io.containerd.grpc.v1.cri".containerd]
  default_runtime_name = "runc"
  discard_unpacked_layers = true
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc]
  runtime_type = "io.containerd.runc.v2"

[plugins."io.containerd.grpc.v1.cri".registry]
  config_path = "/etc/containerd/certs.d"
`

const fixtureB = `version = 2
required_plugins = ["io.containerd.grpc.v1.cri"]
# Kubernetes doesn't use containerd restart manager.
disabled_plugins = ["io.containerd.internal.v1.restart"]
oom_score = -999

[debug]
  level = "info"

[grpc]
  gid = 412

[plugins."io.containerd.grpc.v1.cri"]
  enable_cdi = true
  stream_server_address = "127.0.0.1"
  max_container_log_line_size = 262144
  sandbox_image = "gke.gcr.io/pause:3.8@sha256:880e63f94b145e46f1b1082bb71b85e21f16b99b180b9996407d61240ceb9830"
  image_pull_progress_timeout = "5m"
[plugins."io.containerd.grpc.v1.cri".cni]
  bin_dir = "/home/kubernetes/bin"
  conf_dir = "/etc/cni/net.d"
  conf_template = ""
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc.options]
  SystemdCgroup = true
[plugins."io.containerd.grpc.v1.cri".registry.mirrors."docker.io"]
  endpoint = ["https://mirror.gcr.io","https://registry-1.docker.io"]
[metrics]
 address = "127.0.0.1:1338"
[plugins."io.containerd.grpc.v1.cri".containerd]
  default_runtime_name = "runc"
  discard_unpacked_layers = true
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc]
  runtime_type = "io.containerd.runc.v2"
`

const fixtureC = `version = 2

[plugins.'io.containerd.grpc.v1.cri'.registry]
  config_path = ""
`

const expectedOutputC = `version = 2

[plugins.'io.containerd.grpc.v1.cri'.registry]
  config_path = "/etc/containerd/certs.d"
`

const fixtureD = `version = 3

[plugins.'io.containerd.cri.v1.images'.registry]
  config_path = ''
`

const expectedOutputD = `version = 3

[plugins.'io.containerd.cri.v1.images'.registry]
  config_path = "/etc/containerd/certs.d"
`

const fixtureV4 = `version = 4

[plugins.'io.containerd.cri.v1.images'.registry]
  config_path = ''
`

const expectedOutputV4 = `version = 4

[plugins.'io.containerd.cri.v1.images'.registry]
  config_path = "/etc/containerd/certs.d"
`

const fixtureV4Configured = `version = 4

[plugins."io.containerd.cri.v1.images".registry]
  config_path = "/etc/containerd/certs.d"
`

const fixtureE = `[plugins."io.containerd.grpc.v1.cri"]
  sandbox_image = "registry.k8s.io/pause:3.9"
`

const expectedOutputE = `[plugins."io.containerd.grpc.v1.cri"]
  sandbox_image = "registry.k8s.io/pause:3.9"

[plugins."io.containerd.grpc.v1.cri".registry]
  config_path = "/etc/containerd/certs.d"
`

func init() {
	// In unit test environments where containerd is not runnable via nsenter,
	// validate config candidate using the TOML parser.
	validateConfigFn = func(tmpPath string) error {
		data, err := os.ReadFile(tmpPath)
		if err != nil {
			return err
		}
		var v any
		return toml.Unmarshal(data, &v)
	}
	restartContainerdFn = func() error {
		return nil
	}
	checkContainerdActiveFn = func() bool {
		return true
	}
}

func validateTOMLOrContainerd(t *testing.T, content string) {
	t.Helper()
	var parsed map[string]any
	if err := toml.Unmarshal([]byte(content), &parsed); err != nil {
		t.Fatalf("output is not valid TOML: %v\nContent:\n%s", err, content)
	}

	if _, err := exec.LookPath("containerd"); err == nil {
		tmpDir := t.TempDir()
		tmpFile := filepath.Join(tmpDir, "config.toml")
		if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write temp file: %v", err)
		}
		cmd := exec.Command("containerd", "-c", tmpFile, "config", "dump")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("containerd config dump failed: %v\nOutput: %s", err, string(out))
		}
	}
}

func TestTransformContainerdConfig_TableDriven(t *testing.T) {
	tests := []struct {
		name            string
		input           string
		expectedOutput  string
		expectedChanged bool
		expectErr       bool
	}{
		{
			name:            "(a) fixture upgrade case: stale v3 removed, mirrors neutralized, v2 certs.d added",
			input:           fixtureA,
			expectedOutput:  expectedOutputA,
			expectedChanged: true,
		},
		{
			name:            "(b) fresh GKE node: fixture minus stale v3 block -> same expected output",
			input:           fixtureB,
			expectedOutput:  expectedOutputA,
			expectedChanged: true,
		},
		{
			name:            "(c) v2 with single-quoted header and empty config_path replaced in place",
			input:           fixtureC,
			expectedOutput:  expectedOutputC,
			expectedChanged: true,
		},
		{
			name:            "(d) v3 with single-quoted header and empty config_path replaced in place",
			input:           fixtureD,
			expectedOutput:  expectedOutputD,
			expectedChanged: true,
		},
		{
			name:            "(d2) v4 with single-quoted header and empty config_path replaced in place",
			input:           fixtureV4,
			expectedOutput:  expectedOutputV4,
			expectedChanged: true,
		},
		{
			name:            "(d3) v4 with existing certs.d config_path is a no-op",
			input:           fixtureV4Configured,
			expectedOutput:  fixtureV4Configured,
			expectedChanged: false,
		},
		{
			name:            "(e) no version key: grpc.v1.cri table used",
			input:           fixtureE,
			expectedOutput:  expectedOutputE,
			expectedChanged: true,
		},
		{
			name:            "(f) running on the output of (a): idempotent no-op",
			input:           expectedOutputA,
			expectedOutput:  expectedOutputA,
			expectedChanged: false,
		},
		{
			name:      "invalid non-integer version returns error",
			input:     `version = "invalid"` + "\n",
			expectErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotOutput, gotChanged, _, err := transformContainerdConfig(tc.input, defaultCertsDDirectory)
			if tc.expectErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("transformContainerdConfig failed: %v", err)
			}
			if gotChanged != tc.expectedChanged {
				t.Errorf("expected changed=%v, got %v", tc.expectedChanged, gotChanged)
			}
			if diff := cmp.Diff(tc.expectedOutput, gotOutput); diff != "" {
				t.Errorf("output mismatch (-want +got):\n%s", diff)
			}
			validateTOMLOrContainerd(t, gotOutput)
		})
	}
}

func TestUpdateContainerdConfig_FullLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	certsDir := filepath.Join(tmpDir, "certs.d")

	origCertsDir := certsDDirectory
	certsDDirectory = certsDir
	defer func() {
		certsDDirectory = origCertsDir
	}()

	if err := os.WriteFile(configPath, []byte(fixtureA), 0644); err != nil {
		t.Fatalf("failed to write initial config: %v", err)
	}

	// 1. First run: should change config, create backup, translate mirrors
	changed, err := updateContainerdConfig(configPath)
	if err != nil {
		t.Fatalf("updateContainerdConfig failed: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true on first run, got false")
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read updated config: %v", err)
	}
	if diff := cmp.Diff(expectedOutputA, string(content)); diff != "" {
		t.Errorf("updated config mismatch (-want +got):\n%s", diff)
	}

	backupPath := configPath + ".in-cluster-image-registry.bak"
	backupContent, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("expected backup file %s to exist: %v", backupPath, err)
	}
	if string(backupContent) != fixtureA {
		t.Errorf("backup file content mismatch; expected original fixtureA")
	}

	// Check translated mirror in certs.d/docker.io/hosts.toml (upstream server is registry-1.docker.io)
	mirrorHostsPath := filepath.Join(certsDir, "docker.io", "hosts.toml")
	mirrorContent, err := os.ReadFile(mirrorHostsPath)
	if err != nil {
		t.Fatalf("expected translated mirror file %s: %v", mirrorHostsPath, err)
	}
	expectedMirror := `server = "https://registry-1.docker.io"

[host."https://mirror.gcr.io"]
  capabilities = ["pull", "resolve"]

[host."https://registry-1.docker.io"]
  capabilities = ["pull", "resolve"]

`
	if diff := cmp.Diff(expectedMirror, string(mirrorContent)); diff != "" {
		t.Errorf("translated mirror hosts.toml mismatch (-want +got):\n%s", diff)
	}

	// 2. Second run on same file: should be no-op (idempotence)
	changed, err = updateContainerdConfig(configPath)
	if err != nil {
		t.Fatalf("second updateContainerdConfig failed: %v", err)
	}
	if changed {
		t.Errorf("expected changed=false on second run, got true")
	}

	backupAfterSecondRun, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("failed to read backup: %v", err)
	}
	if string(backupAfterSecondRun) != fixtureA {
		t.Errorf("backup file was overwritten on subsequent run!")
	}
}

func TestTranslateMirrorConfig(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name           string
		host           string
		endpoints      []string
		expectedServer string
	}{
		{
			name:           "docker.io uses upstream registry-1.docker.io",
			host:           "docker.io",
			endpoints:      []string{"https://mirror.gcr.io", "https://registry-1.docker.io"},
			expectedServer: "https://registry-1.docker.io",
		},
		{
			name:           "quay.io uses quay.io as server",
			host:           "quay.io",
			endpoints:      []string{"https://mirror.example.com"},
			expectedServer: "https://quay.io",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := translateMirrorConfig(tmpDir, tc.host, tc.endpoints); err != nil {
				t.Fatalf("translateMirrorConfig failed: %v", err)
			}
			path := filepath.Join(tmpDir, tc.host, "hosts.toml")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("failed to read %s: %v", path, err)
			}
			content := string(data)
			if !strings.Contains(content, `server = "`+tc.expectedServer+`"`) {
				t.Errorf("expected server %q in content:\n%s", tc.expectedServer, content)
			}
			for _, ep := range tc.endpoints {
				if !strings.Contains(content, `[host."`+ep+`"]`) {
					t.Errorf("expected endpoint %q in content:\n%s", ep, content)
				}
			}
		})
	}
}

func TestRestartAndVerifyContainerd_Rollback(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	backupPath := configPath + ".in-cluster-image-registry.bak"

	firstEverBackup := "first ever backup content\n"
	recentPreWrite := "recent pre-write content\n"
	badContent := "broken containerd config\n"

	if err := os.WriteFile(configPath, []byte(badContent), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	if err := os.WriteFile(backupPath, []byte(firstEverBackup), 0644); err != nil {
		t.Fatalf("failed to write backup: %v", err)
	}

	origTimeout := containerdHealthTimeout
	containerdHealthTimeout = 50 * time.Millisecond
	defer func() {
		containerdHealthTimeout = origTimeout
	}()

	origCheck := checkContainerdActiveFn
	checkContainerdActiveFn = func() bool {
		return false
	}
	defer func() {
		checkContainerdActiveFn = origCheck
	}()

	restartCount := 0
	origRestart := restartContainerdFn
	restartContainerdFn = func() error {
		restartCount++
		return nil
	}
	defer func() {
		restartContainerdFn = origRestart
	}()

	// restartAndVerifyContainerd should fail and rollback to recentPreWrite, NOT firstEverBackup
	err := restartAndVerifyContainerd(configPath, recentPreWrite)
	if err == nil {
		t.Fatalf("expected error from restartAndVerifyContainerd, got nil")
	}

	// Verify config was restored to recentPreWrite
	currentContent, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("failed to read configPath after rollback: %v", readErr)
	}
	if string(currentContent) != recentPreWrite {
		t.Errorf("expected rolled back config %q, got %q", recentPreWrite, string(currentContent))
	}

	if restartCount < 2 {
		t.Errorf("expected at least 2 restarts, got %d", restartCount)
	}
}

func TestRestartAndVerifyContainerd_RollbackFallbackToBak(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	backupPath := configPath + ".in-cluster-image-registry.bak"

	firstEverBackup := "first ever backup content\n"
	badContent := "broken containerd config\n"

	if err := os.WriteFile(configPath, []byte(badContent), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	if err := os.WriteFile(backupPath, []byte(firstEverBackup), 0644); err != nil {
		t.Fatalf("failed to write backup: %v", err)
	}

	origTimeout := containerdHealthTimeout
	containerdHealthTimeout = 50 * time.Millisecond
	defer func() {
		containerdHealthTimeout = origTimeout
	}()

	origCheck := checkContainerdActiveFn
	checkContainerdActiveFn = func() bool {
		return false
	}
	defer func() {
		checkContainerdActiveFn = origCheck
	}()

	origRestart := restartContainerdFn
	restartContainerdFn = func() error {
		return nil
	}
	defer func() {
		restartContainerdFn = origRestart
	}()

	// When preWriteContent is empty, rollback should fall back to .bak file
	err := restartAndVerifyContainerd(configPath, "")
	if err == nil {
		t.Fatalf("expected error from restartAndVerifyContainerd, got nil")
	}

	currentContent, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("failed to read configPath after rollback: %v", readErr)
	}
	if string(currentContent) != firstEverBackup {
		t.Errorf("expected fallback rolled back config %q, got %q", firstEverBackup, string(currentContent))
	}
}

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

	// Update with same IP, should not change anything
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
