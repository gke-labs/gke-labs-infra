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

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/e2e"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/fetch"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/spec"
)

func ensureBuildctl(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("buildctl"); err == nil {
		return
	}

	ctx := t.Context()
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	version := "v0.28.0"
	downloadURL := fmt.Sprintf("https://github.com/moby/buildkit/releases/download/%s/buildkit-%s.%s-%s.tar.gz", version, version, goos, goarch)
	tmpDir := t.TempDir()
	archivePath, err := fetch.Download(ctx, downloadURL, "", tmpDir)
	if err != nil {
		t.Fatalf("failed to download buildctl: %v", err)
	}
	defer func() {
		_ = os.Remove(archivePath)
	}()

	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("failed to create bin dir: %v", err)
	}
	buildctlBin := filepath.Join(binDir, "buildctl")
	if err := (spec.TarGz{}).Extract(archivePath, "buildctl", buildctlBin); err != nil {
		t.Fatalf("failed to extract buildctl: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestInClusterBuildKit(t *testing.T) {
	if os.Getenv("RUN_E2E") == "" {
		t.Skip("RUN_E2E not set, skipping e2e test")
	}

	ensureBuildctl(t)

	// If running inside a container with a Kubernetes service account, clear
	// KUBERNETES_SERVICE_HOST so ap uses in-process port-forwarding rather than
	// assuming it is inside the target cluster.
	t.Setenv("KUBERNETES_SERVICE_HOST", "")

	h := e2e.NewHarness(t, e2e.Options{ClusterName: "ap-registry-e2e"})
	h.SetupRegistry()

	repoRoot := h.FindRepoRoot()

	// Deploy BuildKit into autodeploy-system namespace from the repo manifest
	nsManifest := `apiVersion: v1
kind: Namespace
metadata:
  name: autodeploy-system
`
	cmd := exec.Command(h.KubectlCommand(), "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(nsManifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to create autodeploy-system namespace: %v\n%s", err, out)
	}

	buildkitManifest := filepath.Join(repoRoot, "autodeploy", "k8s", "docker-buildkit.yaml")
	h.RunCmd(h.KubectlCommand(), "apply", "-f", buildkitManifest)
	h.RunCmd(h.KubectlCommand(), "wait", "-n", "autodeploy-system", "--for=condition=available", "--timeout=2m", "deployment/buildkit")

	apBin := filepath.Join(t.TempDir(), "ap")
	buildCmd := exec.Command("go", "build", "-o", apBin, "./ap")
	buildCmd.Dir = repoRoot
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build ap: %v\n%s", err, out)
	}

	// Create a throwaway repository with a small image
	tmpDir := t.TempDir()
	h.RunCmd("git", "init", tmpDir)
	h.RunCmd("git", "-C", tmpDir, "config", "user.email", "test@example.com")
	h.RunCmd("git", "-C", tmpDir, "config", "user.name", "Test")

	imageDir := filepath.Join(tmpDir, "images", "tiny")
	if err := os.MkdirAll(imageDir, 0755); err != nil {
		t.Fatalf("failed to create image directory: %v", err)
	}
	dockerfile := "FROM alpine:latest\nRUN echo \"buildkit e2e test\" > /test.txt\n"
	if err := os.WriteFile(filepath.Join(imageDir, "Dockerfile"), []byte(dockerfile), 0644); err != nil {
		t.Fatalf("failed to write Dockerfile: %v", err)
	}

	h.RunCmd("git", "-C", tmpDir, "add", ".")
	h.RunCmd("git", "-C", tmpDir, "commit", "-m", "initial commit")

	t.Setenv("IMAGE_PREFIX", "images.local")
	t.Setenv("IMAGE_TAG", "e2e")

	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chdir(origWd)
	}()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	// Build and push through the in-cluster BuildKit
	h.RunCmd(apBin, "build", "--push", "--buildkit-host", "k8s://autodeploy-system/buildkit")

	// Verify the image is in the in-cluster registry by creating a pod from it (as the registry test does)
	podManifest := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: test-buildkit-pod
  namespace: %s
spec:
  containers:
  - name: test
    image: images.local/tiny:e2e
    command: ["/bin/sh", "-c", "echo hello"]
`, h.Namespace())
	h.KubectlApplyContent(podManifest)

	image := h.RunCmd(h.KubectlCommand(), "get", "pod", "test-buildkit-pod", "-n", h.Namespace(), "-o", "jsonpath={.spec.containers[0].image}")
	expectedImage := "images.local/tiny:e2e"
	if image != expectedImage {
		t.Errorf("expected image %q, got %q", expectedImage, image)
	}

	// Also verify that the image repository and tag exist in the registry API
	tagsOutput := h.RunCmd(h.KubectlCommand(), "exec", "-n", "in-cluster-image-registry-system", "in-cluster-image-registry-0", "--", "wget", "-qO-", "http://localhost:5000/v2/tiny/tags/list")
	if !strings.Contains(tagsOutput, `"name":"tiny"`) || !strings.Contains(tagsOutput, `"e2e"`) {
		t.Errorf("expected in-cluster registry to have tiny:e2e, got: %s", tagsOutput)
	}
}
