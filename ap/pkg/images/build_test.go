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

package images

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

func TestHasImages(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ap-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name     string
		setup    func(root string)
		expected bool
	}{
		{
			name: "no images",
			setup: func(root string) {
				os.MkdirAll(filepath.Join(root, "pkg"), 0755)
			},
			expected: false,
		},
		{
			name: "valid image",
			setup: func(root string) {
				os.MkdirAll(filepath.Join(root, "images", "foo"), 0755)
				os.WriteFile(filepath.Join(root, "images", "foo", "Dockerfile"), []byte("FROM scratch"), 0644)
			},
			expected: true,
		},
		{
			name: "nested image",
			setup: func(root string) {
				os.MkdirAll(filepath.Join(root, "pkg", "images", "foo"), 0755)
				os.WriteFile(filepath.Join(root, "pkg", "images", "foo", "Dockerfile"), []byte("FROM scratch"), 0644)
			},
			expected: true,
		},
		{
			name: "invalid structure",
			setup: func(root string) {
				os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch"), 0644)
			},
			expected: false,
		},
		{
			name: "nested ap root",
			setup: func(root string) {
				// Inner root
				inner := filepath.Join(root, "inner")
				os.MkdirAll(filepath.Join(inner, ".ap"), 0755)
				os.MkdirAll(filepath.Join(inner, "images", "foo"), 0755)
				os.WriteFile(filepath.Join(inner, "images", "foo", "Dockerfile"), []byte("FROM scratch"), 0644)
			},
			expected: false, // Because the only Dockerfile is inside another ap root
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(tmpDir, tt.name)
			os.MkdirAll(root, 0755)
			tt.setup(root)

			got, err := HasImages(root)
			if err != nil {
				t.Fatalf("HasImages() error = %v", err)
			}
			if got != tt.expected {
				t.Errorf("HasImages() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestBuildTasks(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ap-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	os.MkdirAll(filepath.Join(tmpDir, "images", "foo"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "images", "foo", "Dockerfile"), []byte("FROM scratch"), 0644)

	task, err := BuildTasks(tmpDir, false, "")
	if err != nil {
		t.Fatalf("BuildTasks() error = %v", err)
	}

	group, ok := task.(*tasks.Group)
	if !ok {
		t.Fatalf("expected *tasks.Group, got %T", task)
	}

	found := false
	for _, child := range group.Tasks {
		if bt, ok := child.(*DockerBuildTask); ok {
			if bt.ImageName == "foo" {
				found = true
				if bt.Root != tmpDir {
					t.Errorf("expected Root %s, got %s", tmpDir, bt.Root)
				}
				if bt.Dockerfile != filepath.Join("images", "foo", "Dockerfile") {
					t.Errorf("expected Dockerfile %s, got %s", filepath.Join("images", "foo", "Dockerfile"), bt.Dockerfile)
				}
			}
		}
	}

	if !found {
		t.Errorf("did not find DockerBuildTask for foo")
	}
}

func TestDockerBuildTask_Platforms(t *testing.T) {
	// 1. Setup temporary workspace
	tmpDir, err := os.MkdirTemp("", "ap-platforms-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	apDir := filepath.Join(tmpDir, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 2. Mock execCommandContext
	type commandCall struct {
		name string
		args []string
	}
	var calls []commandCall
	driverMock := "docker-container"

	origExec := execCommandContext
	defer func() { execCommandContext = origExec }()

	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "docker" && len(args) > 1 && args[0] == "buildx" && args[1] == "inspect" {
			// Mock 'docker buildx inspect' to simulate support/lack of support for multi-platform
			return exec.CommandContext(ctx, "echo", "Driver: "+driverMock)
		}
		calls = append(calls, commandCall{name: name, args: args})
		// Return a command that always succeeds (e.g. echo)
		return exec.CommandContext(ctx, "echo", "mocked")
	}

	// 3. Test case A: Push is true, no images.yaml configured -> should buildx multi-platform
	task := &DockerBuildTask{
		ImageName:  "test-img",
		Dockerfile: "images/test-img/Dockerfile",
		Root:       tmpDir,
		Push:       true,
	}

	scope := &tasks.APScope{
		RepoRoot: tmpDir,
		Dir:      tmpDir,
	}

	calls = nil
	if err := task.Run(t.Context(), scope); err != nil {
		t.Fatalf("unexpected error running task: %v", err)
	}

	if len(calls) != 1 || calls[0].name != "docker" {
		t.Errorf("expected 1 docker command call, got %v", calls)
	} else {
		argsStr := strings.Join(calls[0].args, " ")
		if !strings.Contains(argsStr, "buildx build") {
			t.Errorf("expected 'buildx build' in args, got: %s", argsStr)
		}
		if !strings.Contains(argsStr, "--platform linux/amd64,linux/arm64") {
			t.Errorf("expected '--platform linux/amd64,linux/arm64' in args, got: %s", argsStr)
		}
		if !strings.Contains(argsStr, "--push") {
			t.Errorf("expected '--push' in args, got: %s", argsStr)
		}
	}

	// 4. Test case B: Push is false, no images.yaml configured -> should use standard docker build, no platform (since there are multiple)
	task.Push = false
	calls = nil

	if err := task.Run(t.Context(), scope); err != nil {
		t.Fatalf("unexpected error running task: %v", err)
	}

	if len(calls) != 1 || calls[0].name != "docker" {
		t.Errorf("expected 1 docker command call, got %v", calls)
	} else {
		argsStr := strings.Join(calls[0].args, " ")
		if !strings.Contains(argsStr, "build") || strings.Contains(argsStr, "buildx") {
			t.Errorf("expected standard 'build' (no buildx) in args, got: %s", argsStr)
		}
		if strings.Contains(argsStr, "--platform") {
			t.Errorf("expected NO '--platform' when Push is false and multiple platforms are configured, got: %s", argsStr)
		}
	}

	// 5. Test case C: Push is false, images.yaml has single platform -> should use standard docker build with --platform
	apYamlContent := `
platforms:
  - amd64
`
	if err := os.WriteFile(filepath.Join(apDir, "images.yaml"), []byte(apYamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	calls = nil
	if err := task.Run(t.Context(), scope); err != nil {
		t.Fatalf("unexpected error running task: %v", err)
	}

	if len(calls) != 1 || calls[0].name != "docker" {
		t.Errorf("expected 1 docker command call, got %v", calls)
	} else {
		argsStr := strings.Join(calls[0].args, " ")
		if !strings.Contains(argsStr, "--platform linux/amd64") {
			t.Errorf("expected '--platform linux/amd64' when single platform is configured, got: %s", argsStr)
		}
	}

	// 6. Test case D: buildctl host configured -> should use buildctl with --opt platform=
	task.BuildkitHost = "127.0.0.1:1234"
	calls = nil

	if err := task.Run(t.Context(), scope); err != nil {
		t.Fatalf("unexpected error running task: %v", err)
	}

	if len(calls) != 1 || calls[0].name != "buildctl" {
		t.Errorf("expected buildctl command, got %v", calls)
	} else {
		argsStr := strings.Join(calls[0].args, " ")
		if !strings.Contains(argsStr, "--opt platform=linux/amd64") {
			t.Errorf("expected '--opt platform=linux/amd64' in buildctl args, got: %s", argsStr)
		}
	}

	// 7. Test case E: Multi-platform NOT supported by default docker driver
	driverMock = "docker"

	// Remove images.yaml to fall back to defaults (which has 2 platforms)
	if err := os.Remove(filepath.Join(apDir, "images.yaml")); err != nil {
		t.Fatal(err)
	}

	task.Push = true
	task.BuildkitHost = ""
	calls = nil

	if err := task.Run(t.Context(), scope); err != nil {
		t.Fatalf("unexpected error running task: %v", err)
	}

	// Because multi-platform is unsupported, it should fall back to single native platform,
	// and since driver is standard 'docker', it should run standard 'docker build' AND standard 'docker push'.
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls (build then push) when multi-platform is unsupported on docker driver, got %d", len(calls))
	}

	buildCall := calls[0]
	if buildCall.name != "docker" {
		t.Errorf("expected first call to be docker, got %s", buildCall.name)
	}
	buildArgsStr := strings.Join(buildCall.args, " ")
	if !strings.Contains(buildArgsStr, "build") || strings.Contains(buildArgsStr, "buildx") {
		t.Errorf("expected standard 'build' (no buildx) in build args, got: %s", buildArgsStr)
	}
	expectedPlatform := "linux/" + runtime.GOARCH
	if !strings.Contains(buildArgsStr, "--platform "+expectedPlatform) {
		t.Errorf("expected fallback platform %s in build args, got: %s", expectedPlatform, buildArgsStr)
	}

	pushCall := calls[1]
	if pushCall.name != "docker" {
		t.Errorf("expected second call to be docker, got %s", pushCall.name)
	}
	pushArgsStr := strings.Join(pushCall.args, " ")
	if !strings.Contains(pushArgsStr, "push") {
		t.Errorf("expected 'push' in push args, got: %s", pushArgsStr)
	}
}
