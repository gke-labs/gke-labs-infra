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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
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

const fixtureV2ExistingConfigPath = `version = 2

[plugins."io.containerd.grpc.v1.cri".registry]
  config_path = "/some/other/dir"
`

const fixtureV3ExistingConfigPath = `version = 3

[plugins."io.containerd.cri.v1.images".registry]
  config_path = "/some/other/dir"
`

const fixtureV4ExistingConfigPath = `version = 4

[plugins."io.containerd.cri.v1.images".registry]
  config_path = "/some/other/dir"
`

const fixtureV2MissingConfigPath = `version = 2

[plugins."io.containerd.grpc.v1.cri".registry]
`

const expectedOutputV2MissingConfigPath = `version = 2

[plugins."io.containerd.grpc.v1.cri".registry]
  config_path = "/etc/containerd/certs.d"
`

const fixtureV3MissingConfigPath = `version = 3

[plugins."io.containerd.cri.v1.images".registry]
`

const expectedOutputV3MissingConfigPath = `version = 3

[plugins."io.containerd.cri.v1.images".registry]
  config_path = "/etc/containerd/certs.d"
`

const fixtureV4MissingConfigPath = `version = 4

[plugins."io.containerd.cri.v1.images".registry]
`

const expectedOutputV4MissingConfigPath = `version = 4

[plugins."io.containerd.cri.v1.images".registry]
  config_path = "/etc/containerd/certs.d"
`

const fixtureV2ExistingWithMirrors = `version = 2

[plugins."io.containerd.grpc.v1.cri".registry]
  config_path = "/custom/certs.d"

[plugins."io.containerd.grpc.v1.cri".registry.mirrors."docker.io"]
  endpoint = ["https://mirror.gcr.io"]
`

const expectedOutputV2ExistingWithMirrors = `version = 2

[plugins."io.containerd.grpc.v1.cri".registry]
  config_path = "/custom/certs.d"

# [plugins."io.containerd.grpc.v1.cri".registry.mirrors."docker.io"]
#   endpoint = ["https://mirror.gcr.io"]
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
	getContainerdStartTimeFn = func() (time.Time, error) {
		return time.Now().Add(1 * time.Hour), nil
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
		name             string
		input            string
		expectedOutput   string
		expectedCertsDir string
		expectedChanged  bool
		expectErr        bool
		dirExists        func(string) bool
	}{
		{
			name:             "(a) fixture upgrade case: stale v3 removed, mirrors neutralized, v2 certs.d added",
			input:            fixtureA,
			expectedOutput:   expectedOutputA,
			expectedCertsDir: defaultCertsDDirectory,
			expectedChanged:  true,
		},
		{
			name:             "(b) fresh GKE node: fixture minus stale v3 block -> same expected output",
			input:            fixtureB,
			expectedOutput:   expectedOutputA,
			expectedCertsDir: defaultCertsDDirectory,
			expectedChanged:  true,
		},
		{
			name:             "(c) v2 with single-quoted header and empty config_path replaced in place",
			input:            fixtureC,
			expectedOutput:   expectedOutputC,
			expectedCertsDir: defaultCertsDDirectory,
			expectedChanged:  true,
		},
		{
			name:             "(d) v3 with single-quoted header and empty config_path replaced in place",
			input:            fixtureD,
			expectedOutput:   expectedOutputD,
			expectedCertsDir: defaultCertsDDirectory,
			expectedChanged:  true,
		},
		{
			name:             "(d2) v4 with single-quoted header and empty config_path replaced in place",
			input:            fixtureV4,
			expectedOutput:   expectedOutputV4,
			expectedCertsDir: defaultCertsDDirectory,
			expectedChanged:  true,
		},
		{
			name:             "(d3) v4 with existing certs.d config_path is a no-op",
			input:            fixtureV4Configured,
			expectedOutput:   fixtureV4Configured,
			expectedCertsDir: defaultCertsDDirectory,
			expectedChanged:  false,
		},
		{
			name:             "(e) no version key: grpc.v1.cri table used",
			input:            fixtureE,
			expectedOutput:   expectedOutputE,
			expectedCertsDir: defaultCertsDDirectory,
			expectedChanged:  true,
		},
		{
			name:             "(f) running on the output of (a): idempotent no-op",
			input:            expectedOutputA,
			expectedOutput:   expectedOutputA,
			expectedCertsDir: defaultCertsDDirectory,
			expectedChanged:  false,
		},
		{
			name:             "(g1) v2 with existing config_path is not changed and uses existing dir",
			input:            fixtureV2ExistingConfigPath,
			expectedOutput:   fixtureV2ExistingConfigPath,
			expectedCertsDir: "/some/other/dir",
			expectedChanged:  false,
		},
		{
			name:             "(g2) v3 with existing config_path is not changed and uses existing dir",
			input:            fixtureV3ExistingConfigPath,
			expectedOutput:   fixtureV3ExistingConfigPath,
			expectedCertsDir: "/some/other/dir",
			expectedChanged:  false,
		},
		{
			name:             "(g3) v4 with existing config_path is not changed and uses existing dir",
			input:            fixtureV4ExistingConfigPath,
			expectedOutput:   fixtureV4ExistingConfigPath,
			expectedCertsDir: "/some/other/dir",
			expectedChanged:  false,
		},
		{
			name:             "(h1) v2 with missing config_path adds default",
			input:            fixtureV2MissingConfigPath,
			expectedOutput:   expectedOutputV2MissingConfigPath,
			expectedCertsDir: defaultCertsDDirectory,
			expectedChanged:  true,
		},
		{
			name:             "(h2) v3 with missing config_path adds default",
			input:            fixtureV3MissingConfigPath,
			expectedOutput:   expectedOutputV3MissingConfigPath,
			expectedCertsDir: defaultCertsDDirectory,
			expectedChanged:  true,
		},
		{
			name:             "(h3) v4 with missing config_path adds default",
			input:            fixtureV4MissingConfigPath,
			expectedOutput:   expectedOutputV4MissingConfigPath,
			expectedCertsDir: defaultCertsDDirectory,
			expectedChanged:  true,
		},
		{
			name:             "(i) v2 with existing config_path and legacy mirrors translates mirrors and preserves config_path",
			input:            fixtureV2ExistingWithMirrors,
			expectedOutput:   expectedOutputV2ExistingWithMirrors,
			expectedCertsDir: "/custom/certs.d",
			expectedChanged:  true,
		},
		{
			name:             "(j1) v3 with colon-separated config_path prefers directory that exists on disk",
			input:            "version = 3\n\n[plugins.\"io.containerd.cri.v1.images\".registry]\n  config_path = \"/nonexistent/first:/custom/second\"\n",
			expectedOutput:   "version = 3\n\n[plugins.\"io.containerd.cri.v1.images\".registry]\n  config_path = \"/nonexistent/first:/custom/second\"\n",
			expectedCertsDir: "/custom/second",
			expectedChanged:  false,
			dirExists: func(p string) bool {
				return p == "/custom/second"
			},
		},
		{
			name:             "(j2) v3 with colon-separated config_path falls back to first entry when neither exists on disk",
			input:            "version = 3\n\n[plugins.\"io.containerd.cri.v1.images\".registry]\n  config_path = \"/nonexistent/first:/custom/second\"\n",
			expectedOutput:   "version = 3\n\n[plugins.\"io.containerd.cri.v1.images\".registry]\n  config_path = \"/nonexistent/first:/custom/second\"\n",
			expectedCertsDir: "/nonexistent/first",
			expectedChanged:  false,
			dirExists: func(p string) bool {
				return false
			},
		},
		{
			name:      "invalid non-integer version returns error",
			input:     `version = "invalid"` + "\n",
			expectErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.dirExists != nil {
				origDirExists := dirExistsFn
				dirExistsFn = tc.dirExists
				defer func() {
					dirExistsFn = origDirExists
				}()
			}
			res, err := transformContainerdConfig(tc.input, defaultCertsDDirectory)
			if tc.expectErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("transformContainerdConfig failed: %v", err)
			}
			if res.Changed != tc.expectedChanged {
				t.Errorf("expected changed=%v, got %v", tc.expectedChanged, res.Changed)
			}
			if tc.expectedCertsDir != "" && res.CertsDir != tc.expectedCertsDir {
				t.Errorf("expected certsDir=%q, got %q", tc.expectedCertsDir, res.CertsDir)
			}
			if diff := cmp.Diff(tc.expectedOutput, res.Content); diff != "" {
				t.Errorf("output mismatch (-want +got):\n%s", diff)
			}
			validateTOMLOrContainerd(t, res.Content)
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
	_, changed, err := updateContainerdConfig(configPath)
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
	expectedOutput := strings.ReplaceAll(expectedOutputA, defaultCertsDDirectory, certsDir)
	if diff := cmp.Diff(expectedOutput, string(content)); diff != "" {
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
	_, changed, err = updateContainerdConfig(configPath)
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

func TestResolveConfigPath(t *testing.T) {
	origDirExists := dirExistsFn
	defer func() {
		dirExistsFn = origDirExists
	}()

	existingDirs := map[string]bool{
		"/custom/exists": true,
		"/dir2":          true,
	}
	dirExistsFn = func(path string) bool {
		return existingDirs[path]
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "whitespace only",
			input:    "   ",
			expected: "",
		},
		{
			name:     "colons only",
			input:    " : : ",
			expected: "",
		},
		{
			name:     "single existing directory",
			input:    "/custom/exists",
			expected: "/custom/exists",
		},
		{
			name:     "single nonexistent directory",
			input:    "/custom/nonexistent",
			expected: "/custom/nonexistent",
		},
		{
			name:     "colon list prefers existing directory",
			input:    "/dir1:/dir2",
			expected: "/dir2",
		},
		{
			name:     "colon list falls back to first entry if none exist",
			input:    "/no1:/no2",
			expected: "/no1",
		},
		{
			name:     "colon list with spaces around colons",
			input:    " /dir1 : /dir2 ",
			expected: "/dir2",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveConfigPath(tc.input)
			if got != tc.expected {
				t.Errorf("resolveConfigPath(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestReconcile_ConfigPathHandling(t *testing.T) {
	ctx := t.Context()
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: namespace,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.96.0.10",
		},
	}

	cases := []struct {
		name                 string
		initialConfig        string
		expectedRestartCount int
		expectConfigChanged  bool
		expectedCertsDirFunc func(tmpDir string) string
		setupDir             func(tmpDir string)
	}{
		{
			name:                 "v2 with existing config_path: not changed, no restart, hosts.toml written under config_path",
			initialConfig:        fixtureV2ExistingConfigPath,
			expectedRestartCount: 0,
			expectConfigChanged:  false,
			expectedCertsDirFunc: func(tmpDir string) string {
				return filepath.Join(tmpDir, "custom-v2-certs")
			},
		},
		{
			name:                 "v3 with existing config_path: not changed, no restart, hosts.toml written under config_path",
			initialConfig:        fixtureV3ExistingConfigPath,
			expectedRestartCount: 0,
			expectConfigChanged:  false,
			expectedCertsDirFunc: func(tmpDir string) string {
				return filepath.Join(tmpDir, "custom-v3-certs")
			},
		},
		{
			name:                 "v4 with existing config_path: not changed, no restart, hosts.toml written under config_path",
			initialConfig:        fixtureV4ExistingConfigPath,
			expectedRestartCount: 0,
			expectConfigChanged:  false,
			expectedCertsDirFunc: func(tmpDir string) string {
				return filepath.Join(tmpDir, "custom-v4-certs")
			},
		},
		{
			name:                 "v2 missing config_path: added default, containerd restarted, hosts.toml written",
			initialConfig:        fixtureV2MissingConfigPath,
			expectedRestartCount: 1,
			expectConfigChanged:  true,
			expectedCertsDirFunc: func(tmpDir string) string {
				return filepath.Join(tmpDir, "default-certs")
			},
		},
		{
			name:                 "v3 missing config_path: added default, containerd restarted, hosts.toml written",
			initialConfig:        fixtureV3MissingConfigPath,
			expectedRestartCount: 1,
			expectConfigChanged:  true,
			expectedCertsDirFunc: func(tmpDir string) string {
				return filepath.Join(tmpDir, "default-certs")
			},
		},
		{
			name:                 "v4 missing config_path: added default, containerd restarted, hosts.toml written",
			initialConfig:        fixtureV4MissingConfigPath,
			expectedRestartCount: 1,
			expectConfigChanged:  true,
			expectedCertsDirFunc: func(tmpDir string) string {
				return filepath.Join(tmpDir, "default-certs")
			},
		},
		{
			name:                 "v2 with existing config_path and legacy mirrors: mirrors translated, config_path preserved, restarted",
			initialConfig:        fixtureV2ExistingWithMirrors,
			expectedRestartCount: 1,
			expectConfigChanged:  true,
			expectedCertsDirFunc: func(tmpDir string) string {
				return filepath.Join(tmpDir, "custom-mirror-certs")
			},
		},
		{
			name: "colon-separated config_path prefers existing directory on disk",
			initialConfig: `version = 3

[plugins."io.containerd.cri.v1.images".registry]
  config_path = "/nonexistent/first:EXISTING_DIR"
`,
			expectedRestartCount: 0,
			expectConfigChanged:  false,
			setupDir: func(tmpDir string) {
				existing := filepath.Join(tmpDir, "existing-colon-dir")
				if err := os.MkdirAll(existing, 0755); err != nil {
					t.Fatalf("failed to create existing dir: %v", err)
				}
			},
			expectedCertsDirFunc: func(tmpDir string) string {
				return filepath.Join(tmpDir, "existing-colon-dir")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.toml")
			defaultDir := filepath.Join(tmpDir, "default-certs")

			origConfigPath := containerdConfigPath
			containerdConfigPath = configPath
			defer func() { containerdConfigPath = origConfigPath }()

			origCertsDDirectory := certsDDirectory
			certsDDirectory = defaultDir
			defer func() { certsDDirectory = origCertsDDirectory }()

			origDirExists := dirExistsFn
			defer func() { dirExistsFn = origDirExists }()

			restarts := 0
			origRestart := restartContainerdFn
			restartContainerdFn = func() error {
				restarts++
				return nil
			}
			defer func() { restartContainerdFn = origRestart }()

			if tc.setupDir != nil {
				tc.setupDir(tmpDir)
			}

			targetCertsDir := tc.expectedCertsDirFunc(tmpDir)

			cfgContent := strings.ReplaceAll(tc.initialConfig, "/some/other/dir", targetCertsDir)
			cfgContent = strings.ReplaceAll(cfgContent, "/custom/certs.d", targetCertsDir)
			cfgContent = strings.ReplaceAll(cfgContent, "EXISTING_DIR", targetCertsDir)

			if err := os.WriteFile(configPath, []byte(cfgContent), 0644); err != nil {
				t.Fatalf("failed to write initial config: %v", err)
			}

			client := fake.NewSimpleClientset(svc)
			err := reconcile(ctx, client)
			if err != nil {
				t.Fatalf("reconcile failed: %v", err)
			}

			if restarts != tc.expectedRestartCount {
				t.Errorf("expected %d restarts, got %d", tc.expectedRestartCount, restarts)
			}

			currentConfig, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("failed to read configPath: %v", err)
			}

			if !tc.expectConfigChanged && string(currentConfig) != cfgContent {
				t.Errorf("expected config.toml to remain unchanged (-want +got):\n%s", cmp.Diff(cfgContent, string(currentConfig)))
			}
			if tc.expectConfigChanged && string(currentConfig) == cfgContent {
				t.Errorf("expected config.toml to be modified, but content was unchanged")
			}

			hostsFile := filepath.Join(targetCertsDir, registryHost, "hosts.toml")
			hostsContent, err := os.ReadFile(hostsFile)
			if err != nil {
				t.Fatalf("expected hosts.toml at %s, but read failed: %v", hostsFile, err)
			}
			if !strings.Contains(string(hostsContent), `server = "http://10.96.0.10"`) {
				t.Errorf("expected ClusterIP in hosts.toml, got:\n%s", string(hostsContent))
			}

			// If legacy mirrors were present in input, verify mirror translation was written under targetCertsDir
			if strings.Contains(cfgContent, "mirrors") {
				mirrorFile := filepath.Join(targetCertsDir, "docker.io", "hosts.toml")
				mirrorData, err := os.ReadFile(mirrorFile)
				if err != nil {
					t.Fatalf("expected translated mirror file at %s: %v", mirrorFile, err)
				}
				if !strings.Contains(string(mirrorData), `[host."https://mirror.gcr.io"]`) {
					t.Errorf("expected mirror.gcr.io in translated mirror file: %s", string(mirrorData))
				}
			}
		})
	}
}

func TestReconcile_RestartWhenConfigNewerThanContainerd(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	certsDir := filepath.Join(tmpDir, "certs.d")

	origConfigPath := containerdConfigPath
	containerdConfigPath = configPath
	defer func() {
		containerdConfigPath = origConfigPath
	}()

	origCertsDir := certsDDirectory
	certsDDirectory = certsDir
	defer func() {
		certsDDirectory = origCertsDir
	}()

	origRestart := restartContainerdFn
	origGetStartTime := getContainerdStartTimeFn
	defer func() {
		restartContainerdFn = origRestart
		getContainerdStartTimeFn = origGetStartTime
	}()

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: namespace,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.96.0.10",
		},
	}
	clientset := fake.NewSimpleClientset(svc)
	ctx := t.Context()

	t0 := time.Date(2026, 11, 3, 14, 0, 0, 0, time.UTC)

	// 1. File mtime after a fake start time -> restart requested.
	// Write already-configured config so updateContainerdConfig returns changed = false.
	if err := os.WriteFile(configPath, []byte(fixtureV4Configured), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	mtimeAfter := t0.Add(10 * time.Minute)
	if err := os.Chtimes(configPath, mtimeAfter, mtimeAfter); err != nil {
		t.Fatalf("failed to set mtime: %v", err)
	}

	restartCount := 0
	restartContainerdFn = func() error {
		restartCount++
		return nil
	}
	fakeStartTime := t0
	getContainerdStartTimeFn = func() (time.Time, error) {
		return fakeStartTime, nil
	}

	if err := reconcile(ctx, clientset); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if restartCount != 1 {
		t.Errorf("expected 1 restart when mtime is after start time, got %d", restartCount)
	}

	// Condition clears itself: after restart, containerd start time is newer than mtime -> no restart.
	fakeStartTime = mtimeAfter.Add(10 * time.Minute)
	if err := reconcile(ctx, clientset); err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}
	if restartCount != 1 {
		t.Errorf("expected restart count to remain 1 when start time updated after mtime, got %d", restartCount)
	}

	// 2. File mtime before fake start time -> no restart.
	mtimeBefore := t0.Add(-10 * time.Minute)
	if err := os.Chtimes(configPath, mtimeBefore, mtimeBefore); err != nil {
		t.Fatalf("failed to set mtime: %v", err)
	}
	fakeStartTime = t0
	restartCount = 0

	if err := reconcile(ctx, clientset); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if restartCount != 0 {
		t.Errorf("expected 0 restarts when mtime is before start time, got %d", restartCount)
	}

	// 3. Agent edit + stale start time -> exactly one restart.
	// Write unconfigured config (empty config_path) so updateContainerdConfig edits the file.
	if err := os.WriteFile(configPath, []byte(fixtureV4), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	fakeStartTime = t0 // stale start time
	restartCount = 0

	if err := reconcile(ctx, clientset); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if restartCount != 1 {
		t.Errorf("expected exactly 1 restart on agent edit + stale start time, got %d", restartCount)
	}
}

func TestParseContainerdStartTime(t *testing.T) {
	expectedMicroTime := time.UnixMicro(1762181229123456)

	tests := []struct {
		name        string
		output      string
		expected    time.Time
		expectError bool
	}{
		{
			name:     "valid ExecMainStartTimestampUSec",
			output:   "ExecMainStartTimestampUSec=1762181229123456\n",
			expected: expectedMicroTime,
		},
		{
			name:        "missing ExecMainStartTimestampUSec",
			output:      "",
			expectError: true,
		},
		{
			name:        "zero ExecMainStartTimestampUSec",
			output:      "ExecMainStartTimestampUSec=0\n",
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseContainerdStartTime(tc.output)
			if tc.expectError {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.Equal(tc.expected) {
				t.Errorf("got %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestIsConfigFileNewerThanContainerd(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	origGetStartTime := getContainerdStartTimeFn
	defer func() {
		getContainerdStartTimeFn = origGetStartTime
	}()

	// 1. File does not exist -> returns false, nil
	newer, err := isConfigFileNewerThanContainerd(filepath.Join(tmpDir, "nonexistent.toml"))
	if err != nil {
		t.Fatalf("unexpected error for nonexistent file: %v", err)
	}
	if newer {
		t.Errorf("expected false for nonexistent file, got true")
	}

	// 2. File exists and mtime > start time -> returns true, nil
	if err := os.WriteFile(configPath, []byte("content"), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	t0 := time.Date(2026, 11, 3, 14, 0, 0, 0, time.UTC)
	mtime := t0.Add(5 * time.Minute)
	if err := os.Chtimes(configPath, mtime, mtime); err != nil {
		t.Fatalf("failed to set mtime: %v", err)
	}
	getContainerdStartTimeFn = func() (time.Time, error) {
		return t0, nil
	}

	newer, err = isConfigFileNewerThanContainerd(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !newer {
		t.Errorf("expected newer=true, got false")
	}

	// 3. File exists and mtime <= start time -> returns false, nil
	getContainerdStartTimeFn = func() (time.Time, error) {
		return mtime.Add(5 * time.Minute), nil
	}

	newer, err = isConfigFileNewerThanContainerd(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newer {
		t.Errorf("expected newer=false, got true")
	}
}
