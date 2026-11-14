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

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/images"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/k8s"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
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
	if cmd.Long == "" {
		t.Error("expected cmd.Long to be set")
	}
	if !strings.Contains(cmd.Long, "k8s/") {
		t.Errorf("expected cmd.Long to describe path selection, got %q", cmd.Long)
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
			Dir:      tempDir,
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
				Dir:      tempDir,
				DryRun:   true,
			},
			kindCluster: "my-cluster",
		}
		if err := opt.Resolve(nil); err != nil {
			t.Fatalf("opt.Resolve failed: %v", err)
		}
		r, err := opt.GetSourceTree()
		if err != nil {
			t.Fatalf("opt.GetSourceTree failed: %v", err)
		}

		cfg, err := config.Load(opt.RepoRoot)
		if err != nil {
			t.Fatalf("config.Load failed: %v", err)
		}
		naming := images.ResolveImageNaming(cfg)

		allTasks, err := BuildDeployPlan(opt, r, "", naming)
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
		if got := kindLoads[0].GetName(); got != "kind-load (cluster: my-cluster): 2 images" {
			t.Errorf("GetName() = %q, want %q", got, "kind-load (cluster: my-cluster): 2 images")
		}
		var gotImages []string
		for _, img := range kindLoads[0].Images {
			gotImages = append(gotImages, img.String())
		}
		expectedImages := []string{"images.local/a:v1.0", "images.local/b:v1.0"}
		if !reflect.DeepEqual(gotImages, expectedImages) {
			t.Errorf("images = %v, want %v", gotImages, expectedImages)
		}
		var childNames []string
		for _, child := range kindLoads[0].GetChildren() {
			childNames = append(childNames, child.GetName())
		}
		if !reflect.DeepEqual(childNames, expectedImages) {
			t.Errorf("children = %v, want %v", childNames, expectedImages)
		}
		for i, push := range buildTasksPush {
			if push {
				t.Errorf("buildTasksPush[%d] = true, want false (push disabled in kind mode)", i)
			}
		}
	})

	t.Run("allows unset or latest IMAGE_TAG", func(t *testing.T) {
		for _, tag := range []string{"", "latest"} {
			t.Run(fmt.Sprintf("tag=%q", tag), func(t *testing.T) {
				tempDir := setupFixtureRepo(t)
				t.Setenv("IMAGE_TAG", tag)

				opt := DeployOptions{
					RootOptions: &RootOptions{
						RepoRoot: tempDir,
						Dir:      tempDir,
						DryRun:   true,
					},
					kindCluster: "my-cluster",
				}
				if err := opt.Resolve(nil); err != nil {
					t.Fatalf("opt.Resolve failed: %v", err)
				}
				r, err := opt.GetSourceTree()
				if err != nil {
					t.Fatalf("opt.GetSourceTree failed: %v", err)
				}

				cfg, err := config.Load(opt.RepoRoot)
				if err != nil {
					t.Fatalf("config.Load failed: %v", err)
				}
				naming := images.ResolveImageNaming(cfg)

				allTasks, err := BuildDeployPlan(opt, r, "", naming)
				if err != nil {
					t.Fatalf("expected success for tag %q, got error: %v", tag, err)
				}
				if len(allTasks) == 0 {
					t.Fatalf("expected tasks for tag %q, got none", tag)
				}
			})
		}
	})
}

func TestParseClusterFlag(t *testing.T) {
	tests := []struct {
		name        string
		flag        string
		wantContext string
		wantErr     bool
	}{
		{
			name:        "empty",
			flag:        "",
			wantContext: "",
			wantErr:     false,
		},
		{
			name:        "kind://name",
			flag:        "kind://foo",
			wantContext: "kind-foo",
			wantErr:     false,
		},
		{
			name:        "plain context",
			flag:        "my-context",
			wantContext: "my-context",
			wantErr:     false,
		},
		{
			name:        "plain context with kind prefix",
			flag:        "kind-bar",
			wantContext: "kind-bar",
			wantErr:     false,
		},
		{
			name:    "bad scheme",
			flag:    "unknown://foo",
			wantErr: true,
		},
		{
			name:    "kind scheme missing name",
			flag:    "kind://",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotContext, err := ParseClusterFlag(tt.flag)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseClusterFlag(%q) error = %v, wantErr %v", tt.flag, err, tt.wantErr)
			}
			if err != nil {
				errMsg := err.Error()
				if !strings.Contains(errMsg, "kind://<name>") || !strings.Contains(errMsg, "<context>") {
					t.Errorf("ParseClusterFlag(%q) error = %q, expected error naming 'kind://<name>' and '<context>'", tt.flag, errMsg)
				}
				return
			}
			if gotContext != tt.wantContext {
				t.Errorf("ParseClusterFlag(%q) = %q, want %q", tt.flag, gotContext, tt.wantContext)
			}
		})
	}
}

func TestResolveTargetCluster(t *testing.T) {
	tests := []struct {
		name           string
		clusterFlag    string
		currentContext string
		wantCluster    k8s.KubeCluster
		wantErr        bool
	}{
		{
			name:           "empty flag with non-kind current context",
			clusterFlag:    "",
			currentContext: "my-context",
			wantCluster:    k8s.CurrentCluster(),
		},
		{
			name:           "empty flag with kind current context",
			clusterFlag:    "",
			currentContext: "kind-dev",
			wantCluster:    k8s.CurrentKindCluster("dev"),
		},
		{
			name:           "kind flag",
			clusterFlag:    "kind://foo",
			currentContext: "other",
			wantCluster:    k8s.KindCluster("foo"),
		},
		{
			name:           "plain context flag",
			clusterFlag:    "prod-cluster",
			currentContext: "kind-dev",
			wantCluster:    k8s.ClusterFromContext("prod-cluster"),
		},
		{
			name:           "kind prefix context flag",
			clusterFlag:    "kind-bar",
			currentContext: "other",
			wantCluster:    k8s.KindCluster("bar"),
		},
		{
			name:        "invalid scheme",
			clusterFlag: "unknown://foo",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc, err := ResolveTargetCluster(tt.clusterFlag, tt.currentContext)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ResolveTargetCluster(%q, %q) error = %v, wantErr %v", tt.clusterFlag, tt.currentContext, err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if tc != tt.wantCluster {
				t.Errorf("tc = %v, want %v", tc, tt.wantCluster)
			}
		})
	}
}

func TestBuildDeployPlan_DryRunClusterKind(t *testing.T) {
	tempDir := setupFixtureRepo(t)
	t.Setenv("IMAGE_TAG", "v1.0")

	opt := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			Dir:      tempDir,
			DryRun:   true,
		},
		Cluster: "kind://foo",
	}
	if err := opt.Resolve(nil); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}
	r, err := opt.GetSourceTree()
	if err != nil {
		t.Fatalf("opt.GetSourceTree failed: %v", err)
	}

	cfg, err := config.Load(tempDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	naming := images.ResolveImageNaming(cfg)

	allTasks, err := BuildDeployPlan(opt, r, "", naming)
	if err != nil {
		t.Fatalf("BuildDeployPlan failed: %v", err)
	}

	var kindLoads []*KindLoadTask
	var buildTasksPush []bool
	var applyTasks []*k8s.KubectlApplyTask

	for _, task := range allTasks {
		if group, ok := task.(*tasks.Group); ok {
			for _, child := range group.Tasks {
				if pf, ok := child.(*k8s.PortForwardTask); ok {
					child = pf.Child
				}
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
				if dg, ok := child.(*tasks.Group); ok && dg.Name == "deploy-k8s" {
					for _, dt := range dg.Tasks {
						if kat, ok := dt.(*k8s.KubectlApplyTask); ok {
							applyTasks = append(applyTasks, kat)
						}
					}
				}
			}
		}
	}

	// 1. build (no push in kind mode)
	if len(buildTasksPush) == 0 {
		t.Fatal("expected build tasks, got 0")
	}
	for i, push := range buildTasksPush {
		if push {
			t.Errorf("buildTasksPush[%d] = true, want false (push disabled in kind mode)", i)
		}
	}

	// 2. kind-load (cluster: foo)
	if len(kindLoads) != 1 {
		t.Fatalf("expected 1 kind load task, got %d", len(kindLoads))
	}
	if kindLoads[0].ClusterName != "foo" {
		t.Errorf("cluster = %q, want 'foo'", kindLoads[0].ClusterName)
	}
	if !strings.HasPrefix(kindLoads[0].GetName(), "kind-load (cluster: foo)") {
		t.Errorf("kindLoad.GetName() = %q, want prefix 'kind-load (cluster: foo)'", kindLoads[0].GetName())
	}

	// 3. applies with --context kind-foo
	if len(applyTasks) == 0 {
		t.Fatal("expected kubectl apply tasks, got 0")
	}
	for _, at := range applyTasks {
		if at.Cluster != k8s.KindCluster("foo") {
			t.Errorf("applyTask.Cluster = %v, want %v", at.Cluster, k8s.KindCluster("foo"))
		}
	}
}

func TestBuildDeployPlan_DryRunClusterNonKind(t *testing.T) {
	tempDir := setupFixtureRepo(t)

	opt := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			Dir:      tempDir,
			DryRun:   true,
		},
		Cluster: "my-custom-context",
	}
	if err := opt.Resolve(nil); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}
	r, err := opt.GetSourceTree()
	if err != nil {
		t.Fatalf("opt.GetSourceTree failed: %v", err)
	}

	cfg, err := config.Load(tempDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	naming := images.ResolveImageNaming(cfg)

	allTasks, err := BuildDeployPlan(opt, r, "", naming)
	if err != nil {
		t.Fatalf("BuildDeployPlan failed: %v", err)
	}

	var kindLoads []*KindLoadTask
	var buildTasksPush []bool
	var applyTasks []*k8s.KubectlApplyTask

	for _, task := range allTasks {
		if group, ok := task.(*tasks.Group); ok {
			for _, child := range group.Tasks {
				if pf, ok := child.(*k8s.PortForwardTask); ok {
					child = pf.Child
				}
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
				if dg, ok := child.(*tasks.Group); ok && dg.Name == "deploy-k8s" {
					for _, dt := range dg.Tasks {
						if kat, ok := dt.(*k8s.KubectlApplyTask); ok {
							applyTasks = append(applyTasks, kat)
						}
					}
				}
			}
		}
	}

	// 1. push path is planned (push = true)
	if len(buildTasksPush) == 0 {
		t.Fatal("expected build tasks, got 0")
	}
	for i, push := range buildTasksPush {
		if !push {
			t.Errorf("buildTasksPush[%d] = false, want true (push enabled for non-kind)", i)
		}
	}

	// 2. no kind-load
	if len(kindLoads) != 0 {
		t.Errorf("expected 0 kind load tasks, got %d", len(kindLoads))
	}

	// 3. applies with --context my-custom-context
	if len(applyTasks) == 0 {
		t.Fatal("expected kubectl apply tasks, got 0")
	}
	for _, at := range applyTasks {
		if at.Cluster != k8s.ClusterFromContext("my-custom-context") {
			t.Errorf("applyTask.Cluster = %v, want %v", at.Cluster, k8s.ClusterFromContext("my-custom-context"))
		}
	}
}

func collectBuildTasks(plan []tasks.Task) (*k8s.PortForwardTask, []*images.DockerBuildTask) {
	var portForward *k8s.PortForwardTask
	var buildTasks []*images.DockerBuildTask
	for _, task := range plan {
		if group, ok := task.(*tasks.Group); ok {
			for _, child := range group.Tasks {
				if pf, ok := child.(*k8s.PortForwardTask); ok {
					portForward = pf
					child = pf.Child
				}
				if bg, ok := child.(*tasks.Group); ok && bg.Name == "build-images" {
					for _, bt := range bg.Tasks {
						if dbt, ok := bt.(*images.DockerBuildTask); ok {
							buildTasks = append(buildTasks, dbt)
						}
					}
				}
			}
		}
	}
	return portForward, buildTasks
}

func TestBuildDeployPlan_PassesContextToBuildTasks(t *testing.T) {
	tempDir := setupFixtureRepo(t)

	opt := DeployOptions{
		RootOptions: &RootOptions{RepoRoot: tempDir, Dir: tempDir, DryRun: true},
		Cluster:     "my-custom-context",
	}
	if err := opt.Resolve(nil); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}
	r, err := opt.GetSourceTree()
	if err != nil {
		t.Fatalf("opt.GetSourceTree failed: %v", err)
	}

	cfg, err := config.Load(tempDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	naming := images.ResolveImageNaming(cfg)

	tests := []struct {
		buildkitHost string
		wantPF       bool
	}{
		{buildkitHost: "", wantPF: true},
		{buildkitHost: "k8s://autodeploy-system/buildkit", wantPF: false},
	}
	for _, tt := range tests {
		plan, err := BuildDeployPlan(opt, r, tt.buildkitHost, naming)
		if err != nil {
			t.Fatalf("BuildDeployPlan(%q) failed: %v", tt.buildkitHost, err)
		}
		portForward, buildTasks := collectBuildTasks(plan)
		if tt.wantPF && (portForward == nil || portForward.Cluster != k8s.ClusterFromContext("my-custom-context")) {
			t.Errorf("host %q: want registry PortForwardTask with cluster 'my-custom-context', got %v", tt.buildkitHost, portForward)
		}
		if len(buildTasks) == 0 {
			t.Fatalf("host %q: expected DockerBuildTasks, got none", tt.buildkitHost)
		}
		for _, bt := range buildTasks {
			if bt.Cluster != k8s.ClusterFromContext("my-custom-context") {
				t.Errorf("host %q: DockerBuildTask.Cluster = %v, want 'my-custom-context'", tt.buildkitHost, bt.Cluster)
			}
		}
	}
}

func TestDeploy_FixtureRepo_SelectK8s(t *testing.T) {
	tempDir := setupFixtureRepo(t)

	opt := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			Dir:      tempDir,
			DryRun:   true,
		},
		BuildkitHost: "k8s://autodeploy-system/buildkit",
	}

	if err := opt.Resolve([]string{"./k8s"}); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}

	r, err := opt.GetSourceTree()
	if err != nil {
		t.Fatalf("GetSourceTree failed: %v", err)
	}

	naming := images.ResolveImageNaming(nil)
	planTasks, err := BuildDeployPlan(opt, r, opt.BuildkitHost, naming)
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
			Dir:      tempDir,
			DryRun:   true,
		},
		BuildkitHost: "k8s://autodeploy-system/buildkit",
	}

	if err := opt.Resolve([]string{"."}); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}

	r, err := opt.GetSourceTree()
	if err != nil {
		t.Fatalf("GetSourceTree failed: %v", err)
	}

	naming := images.ResolveImageNaming(nil)
	planTasks, err := BuildDeployPlan(opt, r, opt.BuildkitHost, naming)
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
			Dir:      tempDir,
			DryRun:   true,
		},
		BuildkitHost: "k8s://autodeploy-system/buildkit",
	}

	if err := opt.Resolve([]string{"sub/k8s"}); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}

	r, err := opt.GetSourceTree()
	if err != nil {
		t.Fatalf("GetSourceTree failed: %v", err)
	}

	naming := images.ResolveImageNaming(nil)
	planTasks, err := BuildDeployPlan(opt, r, opt.BuildkitHost, naming)
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
			Dir:      tempDir,
			DryRun:   true,
		},
		BuildkitHost: "k8s://autodeploy-system/buildkit",
	}

	if err := opt.Resolve([]string{"./k8s/a.yaml"}); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}

	r, err := opt.GetSourceTree()
	if err != nil {
		t.Fatalf("GetSourceTree failed: %v", err)
	}

	naming := images.ResolveImageNaming(nil)
	planTasks, err := BuildDeployPlan(opt, r, opt.BuildkitHost, naming)
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
			Dir:      tempDir,
			DryRun:   true,
		},
		BuildkitHost: "k8s://autodeploy-system/buildkit",
	}

	if err := opt.Resolve([]string{"./k8s/cm.yaml"}); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}

	r, err := opt.GetSourceTree()
	if err != nil {
		t.Fatalf("GetSourceTree failed: %v", err)
	}

	naming := images.ResolveImageNaming(nil)
	planTasks, err := BuildDeployPlan(opt, r, opt.BuildkitHost, naming)
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
			Dir:      tempDir,
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
			Dir:      tempDir,
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
			Dir:      tempDir,
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

	r, err := (&RootOptions{RepoRoot: tempDir, Dir: tempDir}).GetSourceTree()
	if err != nil {
		t.Fatal(err)
	}

	// 1. Docker buildx path (BuildkitHost empty)
	optBuildx := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			Dir:      tempDir,
			DryRun:   true,
		},
		BuildkitHost: "",
	}
	if err := optBuildx.Resolve([]string{"./k8s"}); err != nil {
		t.Fatalf("optBuildx.Resolve failed: %v", err)
	}
	naming := images.ResolveImageNaming(nil)
	tasksBuildx, err := BuildDeployPlan(optBuildx, r, "", naming)
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
			Dir:      tempDir,
			DryRun:   true,
		},
		BuildkitHost: customHost,
	}
	if err := optBuildctl.Resolve([]string{"./k8s"}); err != nil {
		t.Fatalf("optBuildctl.Resolve failed: %v", err)
	}
	tasksBuildctl, err := BuildDeployPlan(optBuildctl, r, customHost, naming)
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
	t.Chdir(tempDir)

	rootCmd := BuildRootCommand()
	rootCmd.SetArgs([]string{"deploy", "--dry-run", "--buildkit-host=k8s://autodeploy-system/buildkit", "./k8s"})

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
			Dir:      tempDir,
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
			Dir:      tempDir,
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

	r, err := (&RootOptions{RepoRoot: tempDir, Dir: tempDir}).GetSourceTree()
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
			Dir:      tempDir,
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
			Dir:      tempDir,
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
			Dir:      tempDir,
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
			Dir:      tempDir,
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
			Dir:      tempDir,
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

func TestDeploy_DryRun_WithClusterAndBuildkitHost(t *testing.T) {
	tempDir := setupFixtureRepo(t)

	// 1. With --cluster, --namespace, and --buildkit-host
	opt := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			Dir:      tempDir,
			DryRun:   true,
		},
		BuildkitHost: "k8s://autodeploy-system/buildkit",
		Cluster:      "homebase1",
		Namespace:    "snigateway-system",
	}

	if err := opt.Resolve([]string{"./k8s"}); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}

	out := captureStdout(t, func() {
		if err := RunDeploy(t.Context(), opt); err != nil {
			t.Fatalf("RunDeploy failed: %v", err)
		}
	})

	if !strings.Contains(out, "docker-build-a (buildkit: k8s://autodeploy-system/buildkit)") {
		t.Errorf("expected dry-run to show 'docker-build-a (buildkit: k8s://autodeploy-system/buildkit)', got:\n%s", out)
	}
	if !strings.Contains(out, "kubectl-apply-a.yaml (context: homebase1, namespace: snigateway-system)") {
		t.Errorf("expected dry-run to show 'kubectl-apply-a.yaml (context: homebase1, namespace: snigateway-system)', got:\n%s", out)
	}

	// 2. With --cluster alone (e.g. does-not-exist)
	optCluster := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			Dir:      tempDir,
			DryRun:   true,
		},
		BuildkitHost: "k8s://autodeploy-system/buildkit",
		Cluster:      "does-not-exist",
	}

	if err := optCluster.Resolve([]string{"./k8s"}); err != nil {
		t.Fatalf("optCluster.Resolve failed: %v", err)
	}

	outCluster := captureStdout(t, func() {
		if err := RunDeploy(t.Context(), optCluster); err != nil {
			t.Fatalf("RunDeploy failed: %v", err)
		}
	})

	if !strings.Contains(outCluster, "kubectl-apply-a.yaml (context: does-not-exist)") {
		t.Errorf("expected dry-run to show 'kubectl-apply-a.yaml (context: does-not-exist)', got:\n%s", outCluster)
	}

	// 3. Without --cluster and without buildkit host, output is unchanged
	optPlain := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			Dir:      tempDir,
			DryRun:   true,
		},
	}

	if err := optPlain.Resolve([]string{"./k8s"}); err != nil {
		t.Fatalf("optPlain.Resolve failed: %v", err)
	}

	outPlain := captureStdout(t, func() {
		if err := RunDeploy(t.Context(), optPlain); err != nil {
			t.Fatalf("RunDeploy failed: %v", err)
		}
	})

	if !strings.Contains(outPlain, "docker-build-a\n") {
		t.Errorf("expected dry-run to show unchanged 'docker-build-a', got:\n%s", outPlain)
	}
	if !strings.Contains(outPlain, "kubectl-apply-a.yaml\n") {
		t.Errorf("expected dry-run to show unchanged 'kubectl-apply-a.yaml', got:\n%s", outPlain)
	}
	if strings.Contains(outPlain, "(context:") || strings.Contains(outPlain, "(buildkit:") || strings.Contains(outPlain, "(namespace:") {
		t.Errorf("expected plan without --cluster and without buildkit host to be unchanged, got:\n%s", outPlain)
	}
}

func TestBuildDeployCommand_ClusterFlag(t *testing.T) {
	rootOpt := &RootOptions{}
	cmd := BuildDeployCommand(rootOpt)

	flag := cmd.Flags().Lookup("cluster")
	if flag == nil {
		t.Fatal("expected --cluster flag")
	}
	if flag.DefValue != "" {
		t.Errorf("expected default empty, got %q", flag.DefValue)
	}

	if err := cmd.ParseFlags([]string{"--cluster=kind://dev"}); err != nil {
		t.Fatalf("ParseFlags failed: %v", err)
	}
	val, err := cmd.Flags().GetString("cluster")
	if err != nil || val != "kind://dev" {
		t.Errorf("expected cluster=kind://dev, got %q", val)
	}
}

func TestDeploy_RunDeploy_NonexistentNamespaceFailsEarly(t *testing.T) {
	tempDir := setupFixtureRepo(t)

	opt := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			Dir:      tempDir,
			DryRun:   false,
		},
		Namespace: "nonexistent-ns",
	}

	if err := opt.Resolve([]string{"./k8s"}); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}

	var capturedKubectl string
	restore := k8s.SetCheckNamespaceExistsForTest(func(ctx context.Context, ns string, cluster k8s.KubeCluster, kubectlBin string) error {
		capturedKubectl = kubectlBin
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
	if capturedKubectl == "" {
		t.Errorf("expected non-empty kubectl passed to CheckNamespaceExists, got %q", capturedKubectl)
	}
}

func TestDeploy_DryRun_ResolvedReferences(t *testing.T) {
	tempDir := setupFixtureRepo(t)
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "nonexistent"))

	// 1. Push path with default tag (:latest)
	t.Run("default tag in push path prints resolved reference pinned by digest", func(t *testing.T) {
		t.Setenv("IMAGE_TAG", "")
		opt := DeployOptions{
			RootOptions: &RootOptions{
				RepoRoot: tempDir,
				Dir:      tempDir,
				DryRun:   true,
			},
			BuildkitHost: "k8s://autodeploy-system/buildkit",
		}
		if err := opt.Resolve([]string{"./k8s"}); err != nil {
			t.Fatalf("opt.Resolve failed: %v", err)
		}

		out := captureStdout(t, func() {
			if err := RunDeploy(t.Context(), opt); err != nil {
				t.Fatalf("RunDeploy failed: %v", err)
			}
		})

		if !strings.Contains(out, "images.local/a:latest (pinned by digest)") {
			t.Errorf("expected dry-run output to contain 'images.local/a:latest (pinned by digest)', got:\n%s", out)
		}
	})

	// 2. Explicit unique tag in push path
	t.Run("unique IMAGE_TAG in push path prints resolved tagged reference without suffix", func(t *testing.T) {
		t.Setenv("IMAGE_TAG", "v2.0")
		opt := DeployOptions{
			RootOptions: &RootOptions{
				RepoRoot: tempDir,
				Dir:      tempDir,
				DryRun:   true,
			},
			BuildkitHost: "k8s://autodeploy-system/buildkit",
		}
		if err := opt.Resolve([]string{"./k8s"}); err != nil {
			t.Fatalf("opt.Resolve failed: %v", err)
		}

		out := captureStdout(t, func() {
			if err := RunDeploy(t.Context(), opt); err != nil {
				t.Fatalf("RunDeploy failed: %v", err)
			}
		})

		if !strings.Contains(out, "images.local/a:v2.0") {
			t.Errorf("expected dry-run output to contain 'images.local/a:v2.0', got:\n%s", out)
		}
		if strings.Contains(out, "pinned by digest") || strings.Contains(out, "unique tag") {
			t.Errorf("expected explicit tag not to have suffix, got:\n%s", out)
		}
	})

	// 3. Kind path prints unique tag form
	t.Run("kind mode prints unique tag form", func(t *testing.T) {
		t.Setenv("IMAGE_TAG", "")
		opt := DeployOptions{
			RootOptions: &RootOptions{
				RepoRoot: tempDir,
				Dir:      tempDir,
				DryRun:   true,
			},
			kindCluster: "test-cluster",
		}
		if err := opt.Resolve([]string{"./k8s"}); err != nil {
			t.Fatalf("opt.Resolve failed: %v", err)
		}

		out := captureStdout(t, func() {
			if err := RunDeploy(t.Context(), opt); err != nil {
				t.Fatalf("RunDeploy failed: %v", err)
			}
		})

		if !strings.Contains(out, "images.local/a:latest (unique tag)") {
			t.Errorf("expected dry-run output to contain 'images.local/a:latest (unique tag)', got:\n%s", out)
		}
		if !strings.Contains(out, "kind-load (cluster: test-cluster): 1 images") {
			t.Errorf("expected dry-run output to contain 'kind-load (cluster: test-cluster): 1 images', got:\n%s", out)
		}
		if !strings.Contains(out, "images.local/a:latest\n") {
			t.Errorf("expected dry-run output to list image to load 'images.local/a:latest', got:\n%s", out)
		}
	})
}

func TestSelectManifests_Kustomization(t *testing.T) {
	tempDir := t.TempDir()

	certManagerDir := filepath.Join(tempDir, "cert-manager")
	if err := os.MkdirAll(certManagerDir, 0755); err != nil {
		t.Fatal(err)
	}
	kustomizationPath := filepath.Join(certManagerDir, "kustomization.yaml")
	if err := os.WriteFile(kustomizationPath, []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tree, err := sourcetree.Open(tempDir)
	if err != nil {
		t.Fatal(err)
	}

	// Explicit kustomization file path is selected.
	explicitFilters := []targetFilter{{arg: "cert-manager/kustomization.yaml", path: kustomizationPath}}
	selected, err := selectManifests(tree, tempDir, false, explicitFilters)
	if err != nil {
		t.Fatalf("selectManifests failed for explicit kustomization path: %v", err)
	}
	expectedSelected := []string{kustomizationPath}
	if !reflect.DeepEqual(selected, expectedSelected) {
		t.Errorf("selectManifests = %v, want %v", selected, expectedSelected)
	}

	// Directory containing only kustomization.yaml is not treated as a manifest directory.
	dirFilters := []targetFilter{{arg: "cert-manager", path: certManagerDir}}
	_, err = selectManifests(tree, tempDir, false, dirFilters)
	if err == nil {
		t.Fatalf("expected error for directory containing only kustomization.yaml, got nil")
	}
	expectedErrorMessage := `no manifests found under "cert-manager"`
	if !strings.Contains(err.Error(), expectedErrorMessage) {
		t.Errorf("expected error to contain %q, got: %v", expectedErrorMessage, err)
	}
}

func TestDeploy_Kustomize_DryRun(t *testing.T) {
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

	certManagerDir := filepath.Join(tempDir, "cert-manager")
	if err := os.MkdirAll(certManagerDir, 0755); err != nil {
		t.Fatal(err)
	}
	kustomizationPath := filepath.Join(certManagerDir, "kustomization.yaml")
	if err := os.WriteFile(kustomizationPath, []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n"), 0644); err != nil {
		t.Fatal(err)
	}

	opt := DeployOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			Dir:      tempDir,
			DryRun:   true,
		},
	}
	if err := opt.Resolve([]string{"cert-manager/kustomization.yaml"}); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}

	output := captureStdout(t, func() {
		if err := RunDeploy(t.Context(), opt); err != nil {
			t.Fatalf("RunDeploy failed: %v", err)
		}
	})

	expectedTaskName := "kubectl-apply-cert-manager/kustomization.yaml"
	if !strings.Contains(output, expectedTaskName) {
		t.Errorf("expected dry-run output to contain %q, got:\n%s", expectedTaskName, output)
	}
}
