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

package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/images"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/k8s"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

func TestBuildDeployCommand(t *testing.T) {
	rootOpt := &RootOptions{}
	cmd := BuildDeployCommand(rootOpt)
	if cmd == nil {
		t.Fatal("BuildDeployCommand returned nil")
	}
	if cmd.Use != "deploy [path...]" {
		t.Errorf("Unexpected use string: %q", cmd.Use)
	}

	skipPushFlag := cmd.Flags().Lookup("skip-push")
	if skipPushFlag == nil {
		t.Fatal("expected --skip-push flag")
	}
	if skipPushFlag.DefValue != "false" {
		t.Errorf("expected --skip-push default to be false, got %q", skipPushFlag.DefValue)
	}

	buildkitHostFlag := cmd.Flags().Lookup("buildkit-host")
	if buildkitHostFlag == nil {
		t.Fatal("expected --buildkit-host flag")
	}
	if buildkitHostFlag.DefValue != "" {
		t.Errorf("expected --buildkit-host default to be empty without env, got %q", buildkitHostFlag.DefValue)
	}
}

func TestBuildDeployCommand_BuildkitHostEnv(t *testing.T) {
	t.Setenv("BUILDKIT_HOST", "k8s://autodeploy-system/buildkit")

	rootOpt := &RootOptions{}
	cmd := BuildDeployCommand(rootOpt)
	if cmd == nil {
		t.Fatal("BuildDeployCommand returned nil")
	}

	flag := cmd.Flags().Lookup("buildkit-host")
	if flag == nil {
		t.Fatal("expected --buildkit-host flag")
	}
	// Flag default is empty; environment fallback is handled by ResolveBuildkitHost during execution.
	if flag.DefValue != "" {
		t.Errorf("expected default %q, got %q", "", flag.DefValue)
	}
}

func TestBuildDeployCommand_ParseFlags(t *testing.T) {
	rootOpt := &RootOptions{}
	cmd := BuildDeployCommand(rootOpt)
	if err := cmd.ParseFlags([]string{"--skip-push", "--buildkit-host=k8s://custom/buildkit"}); err != nil {
		t.Fatalf("ParseFlags failed: %v", err)
	}

	skipPush, err := cmd.Flags().GetBool("skip-push")
	if err != nil || !skipPush {
		t.Errorf("expected skip-push=true, got %v (err: %v)", skipPush, err)
	}

	buildkitHost, err := cmd.Flags().GetString("buildkit-host")
	if err != nil || buildkitHost != "k8s://custom/buildkit" {
		t.Errorf("expected buildkit-host=k8s://custom/buildkit, got %q (err: %v)", buildkitHost, err)
	}
}

func TestRunDeploy_DryRun(t *testing.T) {
	tempDir := t.TempDir()

	// Initialize git repo
	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, ".ap", "ap.yaml"), []byte("imagePrefix: images.local\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, "images", "testapp"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "images", "testapp", "Dockerfile"), []byte("FROM scratch\n"), 0644); err != nil {
		t.Fatal(err)
	}

	opt := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoots:  []string{tempDir},
			DryRun:   true,
		},
		BuildkitHost: "k8s://autodeploy-system/buildkit",
	}

	if err := opt.Resolve(nil); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}

	if err := RunDeploy(t.Context(), opt); err != nil {
		t.Fatalf("RunDeploy dry-run failed: %v", err)
	}
}

func setupFixtureRepo(t *testing.T) string {
	t.Helper()
	tempDir := t.TempDir()

	// .git and .ap
	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, ".ap", "ap.yaml"), []byte("imagePrefix: images.local\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// images/a, images/b, images/c (unreferenced)
	for _, img := range []string{"a", "b", "c"} {
		dir := filepath.Join(tempDir, "images", img)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// k8s/a.yaml (references image: a)
	k8sADir := filepath.Join(tempDir, "k8s")
	if err := os.MkdirAll(k8sADir, 0755); err != nil {
		t.Fatal(err)
	}
	manifestA := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app-a
spec:
  template:
    spec:
      containers:
      - name: a
        image: a
`
	if err := os.WriteFile(filepath.Join(k8sADir, "a.yaml"), []byte(manifestA), 0644); err != nil {
		t.Fatal(err)
	}

	// k8s/cm.yaml (references no images)
	manifestCM := `apiVersion: v1
kind: ConfigMap
metadata:
  name: app-cm
data:
  key: value
`
	if err := os.WriteFile(filepath.Join(k8sADir, "cm.yaml"), []byte(manifestCM), 0644); err != nil {
		t.Fatal(err)
	}

	// sub/k8s/b.yaml (references image: b)
	k8sBDir := filepath.Join(tempDir, "sub", "k8s")
	if err := os.MkdirAll(k8sBDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifestB := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app-b
spec:
  template:
    spec:
      containers:
      - name: b
        image: b:latest
`
	if err := os.WriteFile(filepath.Join(k8sBDir, "b.yaml"), []byte(manifestB), 0644); err != nil {
		t.Fatal(err)
	}

	return tempDir
}

func extractDeployTasks(planTasks []tasks.Task) (builtImages []string, appliedManifests []string) {
	for _, t := range planTasks {
		group, ok := t.(*tasks.Group)
		if !ok {
			continue
		}
		for _, child := range group.Tasks {
			if pf, ok := child.(*k8s.PortForwardTask); ok {
				child = pf.Child
			}
			if bg, ok := child.(*tasks.Group); ok && bg.Name == "build-images" {
				for _, bt := range bg.Tasks {
					if dbt, ok := bt.(*images.DockerBuildTask); ok {
						builtImages = append(builtImages, dbt.ImageName)
					}
				}
			}
			if dg, ok := child.(*tasks.Group); ok && dg.Name == "deploy-k8s" {
				for _, dt := range dg.Tasks {
					if kat, ok := dt.(*k8s.KubectlApplyTask); ok {
						appliedManifests = append(appliedManifests, kat.ManifestPath)
					}
				}
			}
		}
	}
	return
}

func TestParseKindClusterFromContext(t *testing.T) {
	tests := []struct {
		context string
		want    string
		wantOK  bool
	}{
		{"kind-mycluster", "mycluster", true},
		{"kind-dev", "dev", true},
		{"kind-", "", false},
		{"gke_project_zone", "", false},
		{"docker-desktop", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := ParseKindClusterFromContext(tt.context)
		if ok != tt.wantOK || got != tt.want {
			t.Errorf("ParseKindClusterFromContext(%q) = (%q, %v), want (%q, %v)", tt.context, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestBuildDeployPlan_KindMode(t *testing.T) {
	t.Setenv("IMAGE_TAG", "v1.0")

	t.Run("kind mode wiring", func(t *testing.T) {
		tempDir := setupFixtureRepo(t)

		opt := DeployOptions{
			RootOptions: &RootOptions{
				RepoRoot: tempDir,
				APRoots:  []string{tempDir},
				DryRun:   true,
			},
			kindCluster: "my-cluster",
		}
		if err := opt.Resolve(nil); err != nil {
			t.Fatalf("opt.Resolve failed: %v", err)
		}
		r, err := opt.GetRepo()
		if err != nil {
			t.Fatalf("opt.GetRepo failed: %v", err)
		}

		allTasks, err := BuildDeployPlan(opt, r, "")
		if err != nil {
			t.Fatalf("BuildDeployPlan failed: %v", err)
		}

		var kindLoads []*KindLoadTask
		var buildTasksPush []bool
		for _, task := range allTasks {
			if group, ok := task.(*tasks.Group); ok {
				for _, child := range group.Tasks {
					if kt, ok := child.(*KindLoadTask); ok {
						kindLoads = append(kindLoads, kt)
					}
					if bg, ok := child.(*tasks.Group); ok && bg.Name == "build-images" {
						for _, bt := range bg.Tasks {
							if dbt, ok := bt.(*images.DockerBuildTask); ok {
								buildTasksPush = append(buildTasksPush, dbt.Push)
							}
						}
					}
				}
			}
		}

		if len(kindLoads) != 1 {
			t.Fatalf("expected 1 kind load task, got %d", len(kindLoads))
		}
		if kindLoads[0].ClusterName != "my-cluster" {
			t.Errorf("cluster = %q, want 'my-cluster'", kindLoads[0].ClusterName)
		}
		expectedImages := []string{"images.local/a:v1.0", "images.local/b:v1.0"}
		if !reflect.DeepEqual(kindLoads[0].Images, expectedImages) {
			t.Errorf("images = %v, want %v", kindLoads[0].Images, expectedImages)
		}
		for i, push := range buildTasksPush {
			if push {
				t.Errorf("buildTasksPush[%d] = true, want false (push disabled in kind mode)", i)
			}
		}
	})

	t.Run("fails fast on unset or latest IMAGE_TAG", func(t *testing.T) {
		for _, tag := range []string{"", "latest"} {
			t.Run(fmt.Sprintf("tag=%q", tag), func(t *testing.T) {
				tempDir := setupFixtureRepo(t)
				t.Setenv("IMAGE_TAG", tag)

				opt := DeployOptions{
					RootOptions: &RootOptions{
						RepoRoot: tempDir,
						APRoots:  []string{tempDir},
						DryRun:   true,
					},
					kindCluster: "my-cluster",
				}
				if err := opt.Resolve(nil); err != nil {
					t.Fatalf("opt.Resolve failed: %v", err)
				}
				r, err := opt.GetRepo()
				if err != nil {
					t.Fatalf("opt.GetRepo failed: %v", err)
				}

				_, err = BuildDeployPlan(opt, r, "")
				if err == nil {
					t.Fatalf("expected error for tag %q, got nil", tag)
				}
				if !strings.Contains(err.Error(), "set IMAGE_TAG to a non-latest value so pods use imagePullPolicy IfNotPresent") {
					t.Errorf("unexpected error message: %v", err)
				}
			})
		}
	})
}

func TestDeploy_FixtureRepo_SelectK8s(t *testing.T) {
	tempDir := setupFixtureRepo(t)

	opt := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoot:   tempDir,
			DryRun:   true,
		},
		BuildkitHost: "k8s://autodeploy-system/buildkit",
	}

	if err := opt.Resolve([]string{"./k8s"}); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}

	r, err := opt.GetRepo()
	if err != nil {
		t.Fatalf("GetRepo failed: %v", err)
	}

	planTasks, err := BuildDeployPlan(opt, r, opt.BuildkitHost)
	if err != nil {
		t.Fatalf("BuildDeployPlan failed: %v", err)
	}

	builtImages, appliedManifests := extractDeployTasks(planTasks)

	expectedImages := []string{"a"}
	expectedManifests := []string{
		filepath.Join(tempDir, "k8s", "a.yaml"),
		filepath.Join(tempDir, "k8s", "cm.yaml"),
	}

	if !reflect.DeepEqual(builtImages, expectedImages) {
		t.Errorf("builtImages = %v, want %v", builtImages, expectedImages)
	}
	if !reflect.DeepEqual(appliedManifests, expectedManifests) {
		t.Errorf("appliedManifests = %v, want %v", appliedManifests, expectedManifests)
	}

	// Also verify RunDeploy dry-run succeeds
	if err := RunDeploy(t.Context(), opt); err != nil {
		t.Fatalf("RunDeploy failed: %v", err)
	}
}

func TestDeploy_FixtureRepo_SelectWholeRoot(t *testing.T) {
	tempDir := setupFixtureRepo(t)

	opt := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoot:   tempDir,
			DryRun:   true,
		},
		BuildkitHost: "k8s://autodeploy-system/buildkit",
	}

	if err := opt.Resolve([]string{"."}); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}

	r, err := opt.GetRepo()
	if err != nil {
		t.Fatalf("GetRepo failed: %v", err)
	}

	planTasks, err := BuildDeployPlan(opt, r, opt.BuildkitHost)
	if err != nil {
		t.Fatalf("BuildDeployPlan failed: %v", err)
	}

	builtImages, appliedManifests := extractDeployTasks(planTasks)

	// Both images a and b should be built. Unreferenced image c must NOT be built!
	expectedImages := []string{"a", "b"}
	expectedManifests := []string{
		filepath.Join(tempDir, "k8s", "a.yaml"),
		filepath.Join(tempDir, "k8s", "cm.yaml"),
		filepath.Join(tempDir, "sub", "k8s", "b.yaml"),
	}

	if !reflect.DeepEqual(builtImages, expectedImages) {
		t.Errorf("builtImages = %v, want %v", builtImages, expectedImages)
	}
	if !reflect.DeepEqual(appliedManifests, expectedManifests) {
		t.Errorf("appliedManifests = %v, want %v", appliedManifests, expectedManifests)
	}

	// Also verify RunDeploy dry-run succeeds
	if err := RunDeploy(t.Context(), opt); err != nil {
		t.Fatalf("RunDeploy failed: %v", err)
	}
}

func TestDeploy_FixtureRepo_SelectSubK8s(t *testing.T) {
	tempDir := setupFixtureRepo(t)

	opt := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoot:   tempDir,
			DryRun:   true,
		},
		BuildkitHost: "k8s://autodeploy-system/buildkit",
	}

	if err := opt.Resolve([]string{"sub/k8s"}); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}

	r, err := opt.GetRepo()
	if err != nil {
		t.Fatalf("GetRepo failed: %v", err)
	}

	planTasks, err := BuildDeployPlan(opt, r, opt.BuildkitHost)
	if err != nil {
		t.Fatalf("BuildDeployPlan failed: %v", err)
	}

	builtImages, appliedManifests := extractDeployTasks(planTasks)

	expectedImages := []string{"b"}
	expectedManifests := []string{filepath.Join(tempDir, "sub", "k8s", "b.yaml")}

	if !reflect.DeepEqual(builtImages, expectedImages) {
		t.Errorf("builtImages = %v, want %v", builtImages, expectedImages)
	}
	if !reflect.DeepEqual(appliedManifests, expectedManifests) {
		t.Errorf("appliedManifests = %v, want %v", appliedManifests, expectedManifests)
	}
}

func TestDeploy_FixtureRepo_SelectFile(t *testing.T) {
	tempDir := setupFixtureRepo(t)

	opt := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoot:   tempDir,
			DryRun:   true,
		},
		BuildkitHost: "k8s://autodeploy-system/buildkit",
	}

	if err := opt.Resolve([]string{"./k8s/a.yaml"}); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}

	r, err := opt.GetRepo()
	if err != nil {
		t.Fatalf("GetRepo failed: %v", err)
	}

	planTasks, err := BuildDeployPlan(opt, r, opt.BuildkitHost)
	if err != nil {
		t.Fatalf("BuildDeployPlan failed: %v", err)
	}

	builtImages, appliedManifests := extractDeployTasks(planTasks)

	expectedImages := []string{"a"}
	expectedManifests := []string{filepath.Join(tempDir, "k8s", "a.yaml")}

	if !reflect.DeepEqual(builtImages, expectedImages) {
		t.Errorf("builtImages = %v, want %v", builtImages, expectedImages)
	}
	if !reflect.DeepEqual(appliedManifests, expectedManifests) {
		t.Errorf("appliedManifests = %v, want %v", appliedManifests, expectedManifests)
	}
}

func TestDeploy_FixtureRepo_SelectNoImages(t *testing.T) {
	tempDir := setupFixtureRepo(t)

	opt := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoot:   tempDir,
			DryRun:   true,
		},
		BuildkitHost: "k8s://autodeploy-system/buildkit",
	}

	if err := opt.Resolve([]string{"./k8s/cm.yaml"}); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}

	r, err := opt.GetRepo()
	if err != nil {
		t.Fatalf("GetRepo failed: %v", err)
	}

	planTasks, err := BuildDeployPlan(opt, r, opt.BuildkitHost)
	if err != nil {
		t.Fatalf("BuildDeployPlan failed: %v", err)
	}

	builtImages, appliedManifests := extractDeployTasks(planTasks)

	if len(builtImages) != 0 {
		t.Errorf("expected 0 built images for ConfigMap, got %v", builtImages)
	}
	expectedManifests := []string{filepath.Join(tempDir, "k8s", "cm.yaml")}
	if !reflect.DeepEqual(appliedManifests, expectedManifests) {
		t.Errorf("appliedManifests = %v, want %v", appliedManifests, expectedManifests)
	}

	// Verify dry-run output does not build any images
	out := captureStdout(t, func() {
		if err := RunDeploy(t.Context(), opt); err != nil {
			t.Fatalf("RunDeploy failed: %v", err)
		}
	})

	if strings.Contains(out, "docker-build-") {
		t.Errorf("expected no docker-build tasks in output, got:\n%s", out)
	}
	if !strings.Contains(out, "kubectl-apply-cm.yaml") {
		t.Errorf("expected output to contain kubectl-apply-cm.yaml, got:\n%s", out)
	}
}

func TestDeploy_ZeroManifestsMatchError(t *testing.T) {
	tempDir := setupFixtureRepo(t)

	// Create an empty directory in the repo
	emptyDir := filepath.Join(tempDir, "empty-dir")
	if err := os.MkdirAll(emptyDir, 0755); err != nil {
		t.Fatal(err)
	}

	opt := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoot:   tempDir,
			DryRun:   true,
		},
	}

	// 1. Target is a file with no manifests (e.g. images/a/Dockerfile)
	err := opt.Resolve([]string{"images/a/Dockerfile"})
	if err == nil {
		t.Fatalf("expected error for target matching 0 manifests, got nil")
	}
	if !strings.Contains(err.Error(), "no manifests found under") {
		t.Errorf("expected error to contain 'no manifests found under', got: %v", err)
	}

	// 2. Target is an existing directory with no manifests
	err = opt.Resolve([]string{"./empty-dir"})
	if err == nil {
		t.Fatalf("expected error for directory matching 0 manifests, got nil")
	}
	if !strings.Contains(err.Error(), "no manifests found under") {
		t.Errorf("expected error to contain 'no manifests found under', got: %v", err)
	}
}

func TestDeploy_MissingDockerfile(t *testing.T) {
	tempDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, ".ap", "ap.yaml"), []byte("imagePrefix: images.local\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Manifest references missing-image which has no Dockerfile
	k8sDir := filepath.Join(tempDir, "k8s")
	if err := os.MkdirAll(k8sDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
      - name: main
        image: missing-image
`
	if err := os.WriteFile(filepath.Join(k8sDir, "manifest.yaml"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}

	opt := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoot:   tempDir,
			DryRun:   true,
		},
	}

	err := opt.Resolve([]string{"."})
	if err == nil {
		t.Fatalf("expected error for missing Dockerfile, got nil")
	}
	if !strings.Contains(err.Error(), "missing-image") {
		t.Errorf("expected error to mention image name 'missing-image', got: %v", err)
	}
	if !strings.Contains(err.Error(), "images/missing-image/Dockerfile") {
		t.Errorf("expected error to mention 'images/missing-image/Dockerfile', got: %v", err)
	}
}

func TestDeploy_NonexistentPath(t *testing.T) {
	tempDir := setupFixtureRepo(t)

	opt := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoot:   tempDir,
			DryRun:   true,
		},
	}

	err := opt.Resolve([]string{"./nonexistent"})
	if err == nil {
		t.Fatalf("expected error for nonexistent path, got nil")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("expected error to mention 'does not exist', got: %v", err)
	}
}

func TestDeploy_BuildkitAndBuildx(t *testing.T) {
	tempDir := setupFixtureRepo(t)

	r, err := (&RootOptions{RepoRoot: tempDir, APRoot: tempDir}).GetRepo()
	if err != nil {
		t.Fatal(err)
	}

	// 1. Docker buildx path (BuildkitHost empty)
	optBuildx := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoot:   tempDir,
			DryRun:   true,
		},
		BuildkitHost: "",
	}
	if err := optBuildx.Resolve([]string{"./k8s"}); err != nil {
		t.Fatalf("optBuildx.Resolve failed: %v", err)
	}
	tasksBuildx, err := BuildDeployPlan(optBuildx, r, "")
	if err != nil {
		t.Fatalf("BuildDeployPlan buildx failed: %v", err)
	}
	for _, tsk := range tasksBuildx {
		group := tsk.(*tasks.Group)
		for _, c := range group.Tasks {
			if pf, ok := c.(*k8s.PortForwardTask); ok {
				c = pf.Child
			}
			if bg, ok := c.(*tasks.Group); ok && bg.Name == "build-images" {
				for _, bt := range bg.Tasks {
					dbt := bt.(*images.DockerBuildTask)
					if dbt.BuildkitHost != "" {
						t.Errorf("expected empty BuildkitHost for buildx, got %q", dbt.BuildkitHost)
					}
				}
			}
		}
	}

	// 2. Buildctl path (BuildkitHost set)
	customHost := "k8s://custom-ns/custom-buildkit"
	optBuildctl := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoot:   tempDir,
			DryRun:   true,
		},
		BuildkitHost: customHost,
	}
	if err := optBuildctl.Resolve([]string{"./k8s"}); err != nil {
		t.Fatalf("optBuildctl.Resolve failed: %v", err)
	}
	tasksBuildctl, err := BuildDeployPlan(optBuildctl, r, customHost)
	if err != nil {
		t.Fatalf("BuildDeployPlan buildctl failed: %v", err)
	}
	for _, tsk := range tasksBuildctl {
		group := tsk.(*tasks.Group)
		for _, c := range group.Tasks {
			if pf, ok := c.(*k8s.PortForwardTask); ok {
				c = pf.Child
			}
			if bg, ok := c.(*tasks.Group); ok && bg.Name == "build-images" {
				for _, bt := range bg.Tasks {
					dbt := bt.(*images.DockerBuildTask)
					if dbt.BuildkitHost != customHost {
						t.Errorf("expected BuildkitHost %q, got %q", customHost, dbt.BuildkitHost)
					}
				}
			}
		}
	}
}

func TestDeploy_CobraCommand_DryRun(t *testing.T) {
	tempDir := setupFixtureRepo(t)

	rootCmd := BuildRootCommand()
	rootCmd.SetArgs([]string{"--root=" + tempDir, "deploy", "--dry-run", "--buildkit-host=k8s://autodeploy-system/buildkit", "./k8s"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute() failed: %v", err)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	defer func() {
		os.Stdout = origStdout
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

func TestDeploy_DryRun_StdoutOutput(t *testing.T) {
	tempDir := setupFixtureRepo(t)

	// 1. ap deploy --dry-run ./k8s
	optK8s := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoot:   tempDir,
			DryRun:   true,
		},
		BuildkitHost: "k8s://autodeploy-system/buildkit",
	}
	if err := optK8s.Resolve([]string{"./k8s"}); err != nil {
		t.Fatalf("optK8s.Resolve failed: %v", err)
	}

	outK8s := captureStdout(t, func() {
		if err := RunDeploy(t.Context(), optK8s); err != nil {
			t.Fatalf("RunDeploy failed: %v", err)
		}
	})

	if !strings.Contains(outK8s, "docker-build-a") {
		t.Errorf("expected outK8s to contain docker-build-a, got:\n%s", outK8s)
	}
	if !strings.Contains(outK8s, "kubectl-apply-a.yaml") {
		t.Errorf("expected outK8s to contain kubectl-apply-a.yaml, got:\n%s", outK8s)
	}
	if strings.Contains(outK8s, "docker-build-b") {
		t.Errorf("expected outK8s to NOT contain docker-build-b, got:\n%s", outK8s)
	}
	if strings.Contains(outK8s, "docker-build-c") {
		t.Errorf("expected outK8s to NOT contain docker-build-c, got:\n%s", outK8s)
	}
	if strings.Contains(outK8s, "b.yaml") {
		t.Errorf("expected outK8s to NOT contain b.yaml, got:\n%s", outK8s)
	}

	// 2. ap deploy --dry-run .
	optDot := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoot:   tempDir,
			DryRun:   true,
		},
		BuildkitHost: "k8s://autodeploy-system/buildkit",
	}
	if err := optDot.Resolve([]string{"."}); err != nil {
		t.Fatalf("optDot.Resolve failed: %v", err)
	}

	outDot := captureStdout(t, func() {
		if err := RunDeploy(t.Context(), optDot); err != nil {
			t.Fatalf("RunDeploy failed: %v", err)
		}
	})

	if !strings.Contains(outDot, "docker-build-a") {
		t.Errorf("expected outDot to contain docker-build-a, got:\n%s", outDot)
	}
	if !strings.Contains(outDot, "docker-build-b") {
		t.Errorf("expected outDot to contain docker-build-b, got:\n%s", outDot)
	}
	if !strings.Contains(outDot, "kubectl-apply-a.yaml") {
		t.Errorf("expected outDot to contain kubectl-apply-a.yaml, got:\n%s", outDot)
	}
	if !strings.Contains(outDot, "kubectl-apply-b.yaml") {
		t.Errorf("expected outDot to contain kubectl-apply-b.yaml, got:\n%s", outDot)
	}
	if strings.Contains(outDot, "docker-build-c") {
		t.Errorf("expected outDot to NOT contain docker-build-c (unreferenced image), got:\n%s", outDot)
	}
}

func TestBuildDeployPlans_Direct(t *testing.T) {
	tempDir := setupFixtureRepo(t)

	r, err := (&RootOptions{RepoRoot: tempDir, APRoot: tempDir}).GetRepo()
	if err != nil {
		t.Fatal(err)
	}

	plans, err := buildDeployPlans(r, tempDir, tempDir, []string{"./k8s"})
	if err != nil {
		t.Fatalf("buildDeployPlans failed: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("expected 1 plan, got %d", len(plans))
	}
	if len(plans[0].manifests) != 2 {
		t.Errorf("expected 2 manifests (a.yaml, cm.yaml), got %d", len(plans[0].manifests))
	}
	if !reflect.DeepEqual(plans[0].imageNames, []string{"a"}) {
		t.Errorf("expected imageNames [a], got %v", plans[0].imageNames)
	}
}

func TestDeploy_ImageReferencedOnlyThroughImagePrefix(t *testing.T) {
	tempDir := t.TempDir()

	// .git and .ap
	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, ".ap", "ap.yaml"), []byte("imagePrefix: images.local\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// images/worker, images/sidecar, images/unreferenced
	for _, img := range []string{"worker", "sidecar", "unreferenced"} {
		dir := filepath.Join(tempDir, "images", img)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Manifest referencing worker only via ${IMAGE_PREFIX}/worker (in args) and sidecar via ${IMAGE_PREFIX}/sidecar:${IMAGE_TAG} (in ConfigMap)
	k8sDir := filepath.Join(tempDir, "k8s")
	if err := os.MkdirAll(k8sDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
      - name: main
        image: gcr.io/fixed/base:v1
        args:
        - --worker-image=${IMAGE_PREFIX}/worker
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
data:
  sidecar.image: "${IMAGE_PREFIX}/sidecar:${IMAGE_TAG}"
`
	if err := os.WriteFile(filepath.Join(k8sDir, "manifest.yaml"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}

	opt := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoot:   tempDir,
			DryRun:   true,
		},
		BuildkitHost: "k8s://autodeploy-system/buildkit",
	}

	if err := opt.Resolve([]string{"."}); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}

	out := captureStdout(t, func() {
		if err := RunDeploy(t.Context(), opt); err != nil {
			t.Fatalf("RunDeploy failed: %v", err)
		}
	})

	if !strings.Contains(out, "docker-build-worker") {
		t.Errorf("expected dry-run output to contain docker-build-worker, got:\n%s", out)
	}
	if !strings.Contains(out, "docker-build-sidecar") {
		t.Errorf("expected dry-run output to contain docker-build-sidecar, got:\n%s", out)
	}
	if strings.Contains(out, "docker-build-unreferenced") {
		t.Errorf("expected dry-run output to NOT contain docker-build-unreferenced, got:\n%s", out)
	}
	if !strings.Contains(out, "kubectl-apply-manifest.yaml") {
		t.Errorf("expected dry-run output to contain kubectl-apply-manifest.yaml, got:\n%s", out)
	}
}

func TestDeploy_MissingDockerfile_ImagePrefix(t *testing.T) {
	tempDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, ".ap", "ap.yaml"), []byte("imagePrefix: images.local\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Manifest references missing-image only via ${IMAGE_PREFIX}/missing-image
	k8sDir := filepath.Join(tempDir, "k8s")
	if err := os.MkdirAll(k8sDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
      - name: main
        image: gcr.io/fixed/base:v1
        env:
        - name: SUB_IMAGE
          value: ${IMAGE_PREFIX}/missing-image
`
	if err := os.WriteFile(filepath.Join(k8sDir, "manifest.yaml"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}

	opt := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoot:   tempDir,
			DryRun:   true,
		},
	}

	err := opt.Resolve([]string{"."})
	if err == nil {
		t.Fatalf("expected error for missing Dockerfile, got nil")
	}
	if !strings.Contains(err.Error(), "missing-image") {
		t.Errorf("expected error to mention image name 'missing-image', got: %v", err)
	}
	if !strings.Contains(err.Error(), "images/missing-image/Dockerfile") {
		t.Errorf("expected error to mention 'images/missing-image/Dockerfile', got: %v", err)
	}
	if !strings.Contains(err.Error(), filepath.Join("k8s", "manifest.yaml")) {
		t.Errorf("expected error to mention manifest path 'k8s/manifest.yaml', got: %v", err)
	}
}

func TestBuildDeployCommand_NamespaceFlags(t *testing.T) {
	rootOpt := &RootOptions{}
	cmd := BuildDeployCommand(rootOpt)

	nsFlag := cmd.Flags().Lookup("namespace")
	if nsFlag == nil {
		t.Fatal("expected --namespace flag")
	}
	if nsFlag.Shorthand != "n" {
		t.Errorf("expected shorthand 'n', got %q", nsFlag.Shorthand)
	}
	if nsFlag.DefValue != "" {
		t.Errorf("expected default empty, got %q", nsFlag.DefValue)
	}

	// Test parsing --namespace
	cmd1 := BuildDeployCommand(&RootOptions{})
	if err := cmd1.ParseFlags([]string{"--namespace=corp-gke"}); err != nil {
		t.Fatalf("ParseFlags failed: %v", err)
	}
	val, err := cmd1.Flags().GetString("namespace")
	if err != nil || val != "corp-gke" {
		t.Errorf("expected namespace=corp-gke, got %q (err: %v)", val, err)
	}

	// Test parsing -n
	cmd2 := BuildDeployCommand(&RootOptions{})
	if err := cmd2.ParseFlags([]string{"-n", "custom-ns"}); err != nil {
		t.Fatalf("ParseFlags failed: %v", err)
	}
	val, err = cmd2.Flags().GetString("namespace")
	if err != nil || val != "custom-ns" {
		t.Errorf("expected namespace=custom-ns, got %q (err: %v)", val, err)
	}
}

func TestDeploy_InvalidNamespace(t *testing.T) {
	tempDir := setupFixtureRepo(t)

	opt := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoot:   tempDir,
			DryRun:   true,
		},
		Namespace: "INVALID_NAME",
	}

	err := opt.Resolve([]string{"."})
	if err == nil {
		t.Fatalf("expected error for invalid namespace, got nil")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected error to mention 'invalid', got: %v", err)
	}
}

func TestDeploy_DryRun_WithNamespace(t *testing.T) {
	tempDir := setupFixtureRepo(t)

	opt := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoot:   tempDir,
			DryRun:   true,
		},
		BuildkitHost: "k8s://autodeploy-system/buildkit",
		Namespace:    "corp-gke",
	}

	if err := opt.Resolve([]string{"./k8s"}); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}

	out := captureStdout(t, func() {
		if err := RunDeploy(t.Context(), opt); err != nil {
			t.Fatalf("RunDeploy failed: %v", err)
		}
	})

	if !strings.Contains(out, "kubectl-apply-a.yaml (namespace: corp-gke)") {
		t.Errorf("expected dry-run to show 'kubectl-apply-a.yaml (namespace: corp-gke)', got:\n%s", out)
	}
	if !strings.Contains(out, "kubectl-apply-cm.yaml (namespace: corp-gke)") {
		t.Errorf("expected dry-run to show 'kubectl-apply-cm.yaml (namespace: corp-gke)', got:\n%s", out)
	}
}

func TestDeploy_DryRun_WithoutNamespace_Unchanged(t *testing.T) {
	tempDir := setupFixtureRepo(t)

	opt := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoot:   tempDir,
			DryRun:   true,
		},
		BuildkitHost: "k8s://autodeploy-system/buildkit",
		Namespace:    "",
	}

	if err := opt.Resolve([]string{"./k8s"}); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}

	out := captureStdout(t, func() {
		if err := RunDeploy(t.Context(), opt); err != nil {
			t.Fatalf("RunDeploy failed: %v", err)
		}
	})

	if !strings.Contains(out, "kubectl-apply-a.yaml\n") {
		t.Errorf("expected dry-run to show unchanged 'kubectl-apply-a.yaml', got:\n%s", out)
	}
	if strings.Contains(out, "(namespace:") {
		t.Errorf("expected dry-run without --namespace to not contain '(namespace:', got:\n%s", out)
	}
}

func TestDeploy_RunDeploy_NonexistentNamespaceFailsEarly(t *testing.T) {
	tempDir := setupFixtureRepo(t)

	opt := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			APRoot:   tempDir,
			DryRun:   false,
		},
		Namespace: "nonexistent-ns",
	}

	if err := opt.Resolve([]string{"./k8s"}); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}

	restore := k8s.SetCheckNamespaceExistsForTest(func(ctx context.Context, ns string) error {
		return fmt.Errorf("target namespace %q does not exist: not found", ns)
	})
	defer restore()

	err := RunDeploy(t.Context(), opt)
	if err == nil {
		t.Fatalf("expected error when namespace does not exist, got nil")
	}
	if !strings.Contains(err.Error(), "target namespace \"nonexistent-ns\" does not exist") {
		t.Errorf("expected error to mention 'target namespace \"nonexistent-ns\" does not exist', got: %v", err)
	}
}
