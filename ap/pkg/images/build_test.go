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
	"reflect"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/k8s"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
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
			name: "nested image in subtree",
			setup: func(root string) {
				// Subtree image
				inner := filepath.Join(root, "inner")
				os.MkdirAll(filepath.Join(inner, "images", "foo"), 0755)
				os.WriteFile(filepath.Join(inner, "images", "foo", "Dockerfile"), []byte("FROM scratch"), 0644)
			},
			expected: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(tmpDir, tt.name)
			os.MkdirAll(root, 0755)
			tt.setup(root)

			r, err := sourcetree.Open(root)
			if err != nil {
				t.Fatalf("sourcetree.Open failed: %v", err)
			}
			got, err := HasImages(r, root)
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

	r, err := sourcetree.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	naming := ResolveImageNaming(nil)
	task, err := BuildTasks(r, tmpDir, false, "", naming)
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

func TestFindImageNames(t *testing.T) {
	tmpDir := t.TempDir()

	os.MkdirAll(filepath.Join(tmpDir, ".ap"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "images", "img-b"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "images", "img-b", "Dockerfile"), []byte("FROM scratch"), 0644)
	os.MkdirAll(filepath.Join(tmpDir, "images", "img-a"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "images", "img-a", "Dockerfile"), []byte("FROM scratch"), 0644)

	// Subtree image should be discovered when searching from root
	os.MkdirAll(filepath.Join(tmpDir, "sub", "images", "img-c"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "sub", "images", "img-c", "Dockerfile"), []byte("FROM scratch"), 0644)

	r, err := sourcetree.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	names, err := FindImageNames(r, tmpDir)
	if err != nil {
		t.Fatalf("FindImageNames failed: %v", err)
	}

	expected := []string{"img-a", "img-b", "img-c"}
	if !reflect.DeepEqual(names, expected) {
		t.Errorf("FindImageNames() = %v, want %v", names, expected)
	}

	// Subtree discovery
	subNames, err := FindImageNames(r, filepath.Join(tmpDir, "sub"))
	if err != nil {
		t.Fatalf("FindImageNames on sub failed: %v", err)
	}
	expectedSub := []string{"img-c"}
	if !reflect.DeepEqual(subNames, expectedSub) {
		t.Errorf("FindImageNames(sub) = %v, want %v", subNames, expectedSub)
	}

	// Duplicate image name should fail
	dupDir := t.TempDir()
	os.MkdirAll(filepath.Join(dupDir, "images", "dup"), 0755)
	os.WriteFile(filepath.Join(dupDir, "images", "dup", "Dockerfile"), []byte("FROM scratch"), 0644)
	os.MkdirAll(filepath.Join(dupDir, "sub", "images", "dup"), 0755)
	os.WriteFile(filepath.Join(dupDir, "sub", "images", "dup", "Dockerfile"), []byte("FROM scratch"), 0644)
	rDup, err := sourcetree.Open(dupDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FindImageNames(rDup, dupDir); err == nil {
		t.Errorf("expected error for duplicate image name, got nil")
	}
}

func TestBuildTasksForImages(t *testing.T) {
	tmpDir := t.TempDir()

	os.MkdirAll(filepath.Join(tmpDir, "images", "a"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "images", "a", "Dockerfile"), []byte("FROM scratch"), 0644)
	os.MkdirAll(filepath.Join(tmpDir, "images", "b"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "images", "b", "Dockerfile"), []byte("FROM scratch"), 0644)

	r, err := sourcetree.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	naming := ResolveImageNaming(nil)

	// 1. Filter for image "a" only
	taskA, err := BuildTasksForImages(r, tmpDir, false, "", []string{"a"}, naming, "")
	if err != nil {
		t.Fatalf("BuildTasksForImages failed: %v", err)
	}
	groupA, ok := taskA.(*tasks.Group)
	if !ok {
		t.Fatalf("expected *tasks.Group, got %T", taskA)
	}
	if len(groupA.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(groupA.Tasks))
	}
	btA, ok := groupA.Tasks[0].(*DockerBuildTask)
	if !ok || btA.ImageName != "a" {
		t.Errorf("expected DockerBuildTask for image a, got %+v", groupA.Tasks[0])
	}
	if btA.BuildkitHost != "" {
		t.Errorf("expected empty BuildkitHost for buildx path, got %q", btA.BuildkitHost)
	}

	// 2. Buildctl path with image "b" only
	buildkitHost := "k8s://autodeploy-system/buildkit"
	taskB, err := BuildTasksForImages(r, tmpDir, true, buildkitHost, []string{"b"}, naming, "build-context")
	if err != nil {
		t.Fatalf("BuildTasksForImages with buildkit failed: %v", err)
	}
	groupB, ok := taskB.(*tasks.Group)
	if !ok {
		t.Fatalf("expected *tasks.Group, got %T", taskB)
	}
	if len(groupB.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(groupB.Tasks))
	}
	btB, ok := groupB.Tasks[0].(*DockerBuildTask)
	if !ok || btB.ImageName != "b" {
		t.Errorf("expected DockerBuildTask for image b, got %+v", groupB.Tasks[0])
	}
	if btB.BuildkitHost != buildkitHost {
		t.Errorf("expected BuildkitHost %q, got %q", buildkitHost, btB.BuildkitHost)
	}
	if !btB.Push {
		t.Errorf("expected Push=true, got %v", btB.Push)
	}
	if btB.KubeContext != "build-context" {
		t.Errorf("expected KubeContext 'build-context', got %q", btB.KubeContext)
	}

	// 3. Filter with empty slice -> 0 tasks
	taskEmpty, err := BuildTasksForImages(r, tmpDir, false, "", []string{}, naming, "")
	if err != nil {
		t.Fatalf("BuildTasksForImages with empty filter failed: %v", err)
	}
	groupEmpty, ok := taskEmpty.(*tasks.Group)
	if !ok {
		t.Fatalf("expected *tasks.Group, got %T", taskEmpty)
	}
	if len(groupEmpty.Tasks) != 0 {
		t.Errorf("expected 0 tasks for empty filter, got %d", len(groupEmpty.Tasks))
	}

	// 4. Filter with nil slice -> 0 tasks (always filters)
	taskNil, err := BuildTasksForImages(r, tmpDir, false, "", nil, naming, "")
	if err != nil {
		t.Fatalf("BuildTasksForImages with nil filter failed: %v", err)
	}
	groupNil, ok := taskNil.(*tasks.Group)
	if !ok {
		t.Fatalf("expected *tasks.Group, got %T", taskNil)
	}
	if len(groupNil.Tasks) != 0 {
		t.Errorf("expected 0 tasks for nil filter, got %d", len(groupNil.Tasks))
	}

	// 5. Port-forward threading with kubeContext for images.local push
	taskPF, err := BuildTasksForImages(r, tmpDir, true, "", []string{"a"}, naming, "cluster-context")
	if err != nil {
		t.Fatalf("BuildTasksForImages failed: %v", err)
	}
	pfTask, ok := taskPF.(*k8s.PortForwardTask)
	if !ok {
		t.Fatalf("expected *k8s.PortForwardTask, got %T", taskPF)
	}
	if pfTask.KubeContext != "cluster-context" {
		t.Errorf("expected PortForwardTask KubeContext 'cluster-context', got %q", pfTask.KubeContext)
	}
	groupPF, ok := pfTask.Child.(*tasks.Group)
	if !ok || len(groupPF.Tasks) != 1 {
		t.Fatalf("expected 1 task in port forward child group, got %v", pfTask.Child)
	}
	btPF, ok := groupPF.Tasks[0].(*DockerBuildTask)
	if !ok || btPF.KubeContext != "cluster-context" {
		t.Errorf("expected DockerBuildTask KubeContext 'cluster-context', got %+v", groupPF.Tasks[0])
	}

	// 5. BuildTasks (without filter) builds all images (a and b)
	taskAll, err := BuildTasks(r, tmpDir, false, "", naming)
	if err != nil {
		t.Fatalf("BuildTasks failed: %v", err)
	}
	groupAll, ok := taskAll.(*tasks.Group)
	if !ok {
		t.Fatalf("expected *tasks.Group, got %T", taskAll)
	}
	if len(groupAll.Tasks) != 2 {
		t.Fatalf("expected 2 tasks for BuildTasks, got %d", len(groupAll.Tasks))
	}
}

func TestBuildTasks_PortForward(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "images", "foo"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "images", "foo", "Dockerfile"), []byte("FROM scratch"), 0644); err != nil {
		t.Fatal(err)
	}

	r, err := sourcetree.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	emptyDir := t.TempDir()
	emptyRepo, err := sourcetree.Open(emptyDir)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name              string
		push              bool
		buildkitHost      string
		envBuildkitHost   string
		imagePrefix       string
		noDockerfiles     bool
		expectPortForward bool
	}{
		{
			name:              "no push",
			push:              false,
			buildkitHost:      "",
			expectPortForward: false,
		},
		{
			name:              "no dockerfiles",
			push:              true,
			buildkitHost:      "",
			noDockerfiles:     true,
			expectPortForward: false,
		},
		{
			name:              "push to images.local without buildkit host",
			push:              true,
			buildkitHost:      "",
			expectPortForward: true,
		},
		{
			name:              "push to images.local with remote tcp buildkit host",
			push:              true,
			buildkitHost:      "tcp://localhost:2375",
			expectPortForward: true,
		},
		{
			name:              "push to images.local with in-cluster k8s:// buildkit host",
			push:              true,
			buildkitHost:      "k8s://autodeploy-system/buildkit",
			expectPortForward: false,
		},
		{
			name:              "push to images.local with shorthand k8s buildkit host",
			push:              true,
			buildkitHost:      "k8s",
			expectPortForward: false,
		},
		{
			name:              "push to images.local with BUILDKIT_HOST env var set to k8s://",
			push:              true,
			buildkitHost:      "",
			envBuildkitHost:   "k8s://autodeploy-system/buildkit",
			expectPortForward: false,
		},
		{
			name:              "push to images.local with BUILDKIT_HOST env var set to k8s",
			push:              true,
			buildkitHost:      "",
			envBuildkitHost:   "k8s",
			expectPortForward: false,
		},
		{
			name:              "push to external registry with no buildkit host",
			push:              true,
			buildkitHost:      "",
			imagePrefix:       "gcr.io/my-project",
			expectPortForward: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BUILDKIT_HOST", tc.envBuildkitHost)
			t.Setenv("IMAGE_PREFIX", tc.imagePrefix)

			resolvedHost, err := ResolveBuildkitHost(tc.buildkitHost)
			if err != nil {
				t.Fatalf("ResolveBuildkitHost() error = %v", err)
			}

			testDir := tmpDir
			testRepo := r
			if tc.noDockerfiles {
				testDir = emptyDir
				testRepo = emptyRepo
			}

			cfg, _ := config.Load(testDir)
			naming := ResolveImageNaming(cfg)
			task, err := BuildTasks(testRepo, testDir, tc.push, resolvedHost, naming)
			if err != nil {
				t.Fatalf("BuildTasks() error = %v", err)
			}

			_, isPortForward := task.(*k8s.PortForwardTask)
			if isPortForward != tc.expectPortForward {
				t.Errorf("expected isPortForward=%v, got %v (%T)", tc.expectPortForward, isPortForward, task)
			}
		})
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
		if (name == "docker-buildx" || name == "docker") && len(args) > 0 && (args[0] == "inspect" || (args[0] == "buildx" && len(args) > 1 && args[1] == "inspect") || (args[0] == "image" && len(args) > 1 && args[1] == "inspect")) {
			// Mock 'inspect' to simulate support/lack of support for multi-platform
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

	r, err := sourcetree.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	scope := tasks.NewTaskContext(tmpDir, tmpDir, r).WithTool("docker-buildx", "docker-buildx").WithTool("docker", "docker")

	calls = nil
	if err := task.Run(t.Context(), scope); err != nil {
		t.Fatalf("unexpected error running task: %v", err)
	}

	if len(calls) != 1 || calls[0].name != "docker-buildx" {
		t.Errorf("expected 1 docker-buildx command call, got %v", calls)
	} else {
		argsStr := strings.Join(calls[0].args, " ")
		if calls[0].args[0] != "build" {
			t.Errorf("expected 'build' as first arg, got: %s", argsStr)
		}
		if !strings.Contains(argsStr, "--platform linux/amd64,linux/arm64") {
			t.Errorf("expected '--platform linux/amd64,linux/arm64' in args, got: %s", argsStr)
		}
		if !strings.Contains(argsStr, "--push") {
			t.Errorf("expected '--push' in args, got: %s", argsStr)
		}
	}

	// 4. Test case B: Push is false, no images.yaml configured -> should use docker buildx build --load, no platform (since there are multiple)
	task.Push = false
	calls = nil

	if err := task.Run(t.Context(), scope); err != nil {
		t.Fatalf("unexpected error running task: %v", err)
	}

	if len(calls) != 1 || calls[0].name != "docker-buildx" {
		t.Errorf("expected 1 docker-buildx command call, got %v", calls)
	} else {
		argsStr := strings.Join(calls[0].args, " ")
		if calls[0].args[0] != "build" {
			t.Errorf("expected 'build' as first arg, got: %s", argsStr)
		}
		if !strings.Contains(argsStr, "--load") {
			t.Errorf("expected '--load' in args, got: %s", argsStr)
		}
		if strings.Contains(argsStr, "--platform") {
			t.Errorf("expected NO '--platform' when Push is false and multiple platforms are configured, got: %s", argsStr)
		}
	}

	// 5. Test case C: Push is false, images.yaml has single platform -> should use docker buildx build with --load and --platform
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

	if len(calls) != 1 || calls[0].name != "docker-buildx" {
		t.Errorf("expected 1 docker-buildx command call, got %v", calls)
	} else {
		argsStr := strings.Join(calls[0].args, " ")
		if calls[0].args[0] != "build" {
			t.Errorf("expected 'build' as first arg, got: %s", argsStr)
		}
		if !strings.Contains(argsStr, "--load") {
			t.Errorf("expected '--load' in args, got: %s", argsStr)
		}
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

	// 7. Test case E: Multi-platform NOT supported by default docker driver and Push is true -> should error
	driverMock = "docker"

	// Remove images.yaml to fall back to defaults (which has 2 platforms)
	if err := os.Remove(filepath.Join(apDir, "images.yaml")); err != nil {
		t.Fatal(err)
	}

	task.Push = true
	task.BuildkitHost = ""
	calls = nil

	if err := task.Run(t.Context(), scope); err == nil {
		t.Fatalf("expected error running task when Push is true and multi-platform is unsupported, got nil")
	} else if !strings.Contains(err.Error(), "multi-platform build") {
		t.Errorf("expected multi-platform support error, got: %v", err)
	}

	// 8. Test case F: Multi-platform NOT initially supported, but creating ap-builder succeeds
	driverMock = "docker"
	createSucceeded := false
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if (name == "docker-buildx" || name == "docker") && len(args) > 0 && args[0] == "inspect" {
			driver := driverMock
			if createSucceeded {
				driver = "docker-container"
			}
			return exec.CommandContext(ctx, "echo", "Driver: "+driver)
		}
		if (name == "docker-buildx" || name == "docker") && len(args) > 0 && args[0] == "create" {
			createSucceeded = true
		}
		calls = append(calls, commandCall{name: name, args: args})
		return exec.CommandContext(ctx, "echo", "mocked")
	}

	calls = nil
	if err := task.Run(t.Context(), scope); err != nil {
		t.Fatalf("unexpected error when builder creation succeeds: %v", err)
	}

	foundBuildxBuild := false
	for _, call := range calls {
		argsStr := strings.Join(call.args, " ")
		if len(call.args) > 0 && call.args[0] == "build" && strings.Contains(argsStr, "--platform linux/amd64,linux/arm64") && strings.Contains(argsStr, "--push") {
			foundBuildxBuild = true
			break
		}
	}
	if !foundBuildxBuild {
		t.Errorf("expected build call with multi-platform and --push after builder creation, calls: %v", calls)
	}

	// 9. Test case G: Multi-platform NOT supported and creating ap-builder fails -> should return wrapped error
	driverMock = "docker"
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if (name == "docker-buildx" || name == "docker") && len(args) > 0 && args[0] == "inspect" {
			return exec.CommandContext(ctx, "echo", "Driver: "+driverMock)
		}
		if (name == "docker-buildx" || name == "docker") && len(args) > 0 && args[0] == "create" {
			return exec.CommandContext(ctx, "false")
		}
		return exec.CommandContext(ctx, "echo", "mocked")
	}

	if err := task.Run(t.Context(), scope); err == nil {
		t.Fatalf("expected error when builder creation fails, got nil")
	} else if !strings.Contains(err.Error(), "failed to ensure multi-platform builder") {
		t.Errorf("expected wrapped error 'failed to ensure multi-platform builder', got: %v", err)
	}
}

func TestResolveBuildkitHost(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		envHost     string
		setEnv      bool
		expected    string
		expectError bool
	}{
		{
			name:        "empty without env",
			input:       "",
			setEnv:      false,
			expected:    "",
			expectError: false,
		},
		{
			name:        "empty with tcp env",
			input:       "",
			envHost:     "tcp://localhost:2375",
			setEnv:      true,
			expected:    "tcp://localhost:2375",
			expectError: false,
		},
		{
			name:        "empty with k8s shorthand env",
			input:       "",
			envHost:     "k8s",
			setEnv:      true,
			expected:    DefaultK8sBuildkitHost,
			expectError: false,
		},
		{
			name:        "k8s shorthand input",
			input:       "k8s",
			setEnv:      false,
			expected:    DefaultK8sBuildkitHost,
			expectError: false,
		},
		{
			name:        "explicit valid k8s url",
			input:       "k8s://custom-ns/custom-buildkit",
			setEnv:      false,
			expected:    "k8s://custom-ns/custom-buildkit",
			expectError: false,
		},
		{
			name:        "explicit non-k8s url",
			input:       "unix:///run/buildkit/buildkitd.sock",
			setEnv:      false,
			expected:    "unix:///run/buildkit/buildkitd.sock",
			expectError: false,
		},
		{
			name:        "explicit arg overrides env",
			input:       "tcp://1.2.3.4:2375",
			envHost:     "k8s",
			setEnv:      true,
			expected:    "tcp://1.2.3.4:2375",
			expectError: false,
		},
		{
			name:        "invalid k8s url missing service",
			input:       "k8s://onlynamespace",
			setEnv:      false,
			expectError: true,
		},
		{
			name:        "invalid k8s url trailing slash",
			input:       "k8s://namespace/",
			setEnv:      false,
			expectError: true,
		},
		{
			name:        "invalid k8s url leading slash",
			input:       "k8s:///service",
			setEnv:      false,
			expectError: true,
		},
		{
			name:        "invalid k8s url extra parts",
			input:       "k8s://namespace/service/extra",
			setEnv:      false,
			expectError: true,
		},
		{
			name:        "invalid k8s url just scheme",
			input:       "k8s://",
			setEnv:      false,
			expectError: true,
		},
		{
			name:        "invalid k8s url in env",
			input:       "",
			envHost:     "k8s://invalid",
			setEnv:      true,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setEnv {
				t.Setenv("BUILDKIT_HOST", tt.envHost)
			} else {
				t.Setenv("BUILDKIT_HOST", "")
			}

			got, err := ResolveBuildkitHost(tt.input)
			if tt.expectError {
				if err == nil {
					t.Fatalf("expected error for input %q, got nil (result: %q)", tt.input, got)
				}
				if !strings.Contains(err.Error(), "invalid buildkit host") {
					t.Errorf("expected error message to mention invalid buildkit host, got: %v", err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error for input %q: %v", tt.input, err)
				}
				if got != tt.expected {
					t.Errorf("ResolveBuildkitHost(%q) = %q, want %q", tt.input, got, tt.expected)
				}
			}
		})
	}
}

func TestDockerBuildTask_ToolDocker(t *testing.T) {
	tmpDir := t.TempDir()

	var executedCmd string
	var executedEnv []string
	origExec := execCommandContext
	defer func() { execCommandContext = origExec }()

	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if len(args) > 0 && args[0] == "build" {
			executedCmd = name
		}
		cmd := exec.CommandContext(ctx, "echo", "mocked")
		return cmd
	}

	mockBuildxBin := "/custom/tools/docker-buildx/bin/docker-buildx"

	task := &DockerBuildTask{
		ImageName:  "test-img",
		Dockerfile: "images/test-img/Dockerfile",
		Root:       tmpDir,
		Push:       false,
	}

	r, err := sourcetree.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	scope := tasks.NewTaskContext(tmpDir, tmpDir, r).WithTool("docker-buildx", mockBuildxBin)

	if err := task.Run(t.Context(), scope); err != nil {
		t.Fatalf("unexpected error running task: %v", err)
	}

	if executedCmd != mockBuildxBin {
		t.Errorf("expected command %q, got %q", mockBuildxBin, executedCmd)
	}

	// Now test with explicit BuildxBin override on task
	explicitBin := "/override/bin/docker-buildx"
	taskExplicit := &DockerBuildTask{
		ImageName:  "test-img-explicit",
		Dockerfile: "images/test-img/Dockerfile",
		Root:       tmpDir,
		Push:       false,
		BuildxBin:  explicitBin,
	}

	if err := taskExplicit.Run(t.Context(), scope); err != nil {
		t.Fatalf("unexpected error running taskExplicit: %v", err)
	}
	if executedCmd != explicitBin {
		t.Errorf("expected command %q, got %q", explicitBin, executedCmd)
	}
	_ = executedEnv
}
