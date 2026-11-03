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
	"path/filepath"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/e2e"
)

func TestInClusterRegistry(t *testing.T) {
	if os.Getenv("RUN_E2E") == "" {
		t.Skip("RUN_E2E not set, skipping e2e test")
	}

	h := e2e.NewHarness(t, e2e.Options{ClusterName: "ap-registry-e2e"})
	h.SetupRegistry()

	repoRoot := h.FindRepoRoot()
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("failed to change to repo root: %v", err)
	}

	// Set up environment for ap
	os.Setenv("IMAGE_PREFIX", "images.local")
	os.Setenv("IMAGE_TAG", "e2e")

	// Create a temporary directory for the self-contained test root
	tmpDir, err := os.MkdirTemp("", "ap-e2e-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Copy autodeploy/examples/helloworld into tmpDir matching repository layout
	targetDir := filepath.Join(tmpDir, "autodeploy", "examples", "helloworld")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(targetDir, os.DirFS(filepath.Join(repoRoot, "autodeploy", "examples", "helloworld"))); err != nil {
		t.Fatalf("failed to copy examples/helloworld to temp dir: %v", err)
	}

	// Deploy using the image
	// We'll create a simple manifest that uses the image
	// examples-helloworld:latest is a placeholder that ap deploy will replace.
	manifest := `
apiVersion: v1
kind: Pod
metadata:
  name: test-pod
  namespace: default
spec:
  containers:
  - name: test
    image: examples-helloworld:latest
    command: ["/bin/sh", "-c", "echo hello"]
`

	if err := os.WriteFile(filepath.Join(targetDir, "k8s", "pod.yaml"), []byte(manifest), 0644); err != nil {
		t.Fatalf("failed to write pod manifest: %v", err)
	}

	h.RunCmd("git", "init", tmpDir)
	h.RunCmd("git", "-C", tmpDir, "config", "user.email", "test@example.com")
	h.RunCmd("git", "-C", tmpDir, "config", "user.name", "Test")
	h.RunCmd("git", "-C", tmpDir, "commit", "--allow-empty", "-m", "init")

	apBin := filepath.Join(t.TempDir(), "ap")
	h.RunCmd("go", "build", "-o", apBin, "./ap")

	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	// Run ap deploy (which builds, pushes, and applies in one root)
	h.RunCmd(apBin, "deploy", "autodeploy/examples/helloworld", "--cluster=kind-ap-registry-e2e")

	// Verify the pod was created and has the correct image
	image := h.RunCmd("kubectl", "get", "pod", "test-pod", "-n", "default", "-o", "jsonpath={.spec.containers[0].image}")
	expectedImage := "images.local/examples-helloworld:e2e"
	if image != expectedImage {
		t.Errorf("expected image %q, got %q", expectedImage, image)
	}
}
