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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/e2e"
)

func TestInClusterBuildKit(t *testing.T) {
	if os.Getenv("RUN_E2E") == "" {
		t.Skip("RUN_E2E not set, skipping e2e test")
	}

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

	t.Chdir(tmpDir)

	// Build and push through the in-cluster BuildKit
	h.RunCmd(apBin, "build", "--push", "--buildkit-host", "k8s://autodeploy-system/buildkit")

	// Verify that the image repository and tag exist in the in-cluster registry
	tagsOutput := h.RunCmd(h.KubectlCommand(), "exec", "-n", "in-cluster-image-registry-system", "in-cluster-image-registry-0", "--", "wget", "-qO-", "http://localhost:5000/v2/tiny/tags/list")
	if !strings.Contains(tagsOutput, `"name":"tiny"`) || !strings.Contains(tagsOutput, `"e2e"`) {
		t.Errorf("expected in-cluster registry to have tiny:e2e, got: %s", tagsOutput)
	}
}
