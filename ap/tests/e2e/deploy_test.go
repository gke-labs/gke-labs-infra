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
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/e2e"
)

func TestDeploy_KindRollout(t *testing.T) {
	if os.Getenv("RUN_E2E") == "" {
		t.Skip("RUN_E2E not set, skipping e2e test")
	}

	h := e2e.NewHarness(t, e2e.Options{ClusterName: "ap-deploy-kind-e2e"})
	ns := h.Namespace()
	imagePrefix := fmt.Sprintf("example.com/%s", ns)

	repoRoot := h.FindRepoRoot()
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("failed to change to repo root: %v", err)
	}

	apBin := filepath.Join(t.TempDir(), "ap")
	h.RunCmd("go", "build", "-o", apBin, "./ap")

	tmpDir, err := os.MkdirTemp("", "ap-deploy-kind-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := os.MkdirAll(filepath.Join(tmpDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}

	origWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".ap", "ap.yaml"), []byte(fmt.Sprintf("imagePrefix: %s\n", imagePrefix)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "images", "my-app"), 0755); err != nil {
		t.Fatal(err)
	}
	dockerfile1 := "FROM alpine:latest\nRUN echo v1 > /version.txt\nCMD [\"/bin/sh\", \"-c\", \"sleep 3600\"]\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "images", "my-app", "Dockerfile"), []byte(dockerfile1), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(tmpDir, "k8s"), 0755); err != nil {
		t.Fatal(err)
	}
	deploymentManifest := fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-app
  namespace: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: my-app
  template:
    metadata:
      labels:
        app: my-app
    spec:
      terminationGracePeriodSeconds: 0
      containers:
      - name: my-app
        image: my-app
        command: ["/bin/sh", "-c", "sleep 3600"]
`, ns)
	if err := os.WriteFile(filepath.Join(tmpDir, "k8s", "deployment.yaml"), []byte(deploymentManifest), 0644); err != nil {
		t.Fatal(err)
	}

	getRSForImage := func(expectedImage string) string {
		out := strings.TrimSpace(h.RunCmd("kubectl", "get", "replicaset", "-l", "app=my-app", "-n", ns, "-o", "jsonpath={range .items[*]}{.metadata.name}{\" \"}{.spec.template.spec.containers[0].image}{\"\\n\"}{end}"))
		for _, line := range strings.Split(out, "\n") {
			parts := strings.Fields(line)
			if len(parts) >= 2 && parts[1] == expectedImage {
				return parts[0]
			}
		}
		t.Fatalf("no replicaset found with template image %q in namespace %s, got:\n%s", expectedImage, ns, out)
		return ""
	}

	// 1. Initial deploy with default tag (:latest) -> unique content tag: latest-<12 hex>
	t.Setenv("IMAGE_TAG", "")
	t.Setenv("IMAGE_PREFIX", imagePrefix)

	h.RunCmd(apBin, "deploy", "--cluster=kind-ap-deploy-kind-e2e")
	h.RunCmd("kubectl", "rollout", "status", "deployment/my-app", "-n", ns, "--timeout=60s")

	appliedImage1 := strings.TrimSpace(h.RunCmd("kubectl", "get", "deployment", "my-app", "-n", ns, "-o", "jsonpath={.spec.template.spec.containers[0].image}"))
	expectedPrefix1 := fmt.Sprintf("%s/my-app:latest-", imagePrefix)
	if !strings.HasPrefix(appliedImage1, expectedPrefix1) {
		t.Fatalf("expected applied image in kind mode to have prefix %q, got %q", expectedPrefix1, appliedImage1)
	}
	rs1 := getRSForImage(appliedImage1)

	// 2. Change Dockerfile and redeploy -> new unique tag: latest-<new 12 hex>
	dockerfile2 := "FROM alpine:latest\nRUN echo v2 > /version.txt\nCMD [\"/bin/sh\", \"-c\", \"sleep 3600\"]\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "images", "my-app", "Dockerfile"), []byte(dockerfile2), 0644); err != nil {
		t.Fatal(err)
	}

	h.RunCmd(apBin, "deploy", "--cluster=kind-ap-deploy-kind-e2e")
	h.RunCmd("kubectl", "rollout", "status", "deployment/my-app", "-n", ns, "--timeout=60s")

	appliedImage2 := strings.TrimSpace(h.RunCmd("kubectl", "get", "deployment", "my-app", "-n", ns, "-o", "jsonpath={.spec.template.spec.containers[0].image}"))
	if appliedImage2 == appliedImage1 {
		t.Errorf("expected applied image to change after redeploy, got %q", appliedImage2)
	}
	if !strings.HasPrefix(appliedImage2, expectedPrefix1) {
		t.Fatalf("expected applied image in kind mode to have prefix %q, got %q", expectedPrefix1, appliedImage2)
	}

	rs2 := getRSForImage(appliedImage2)
	if rs2 == rs1 {
		t.Errorf("expected a new ReplicaSet after redeploy with changed Dockerfile, but both were %q", rs1)
	}

	// 3. Explicit IMAGE_TAG=v1.0 -> exact tag as given (no unique hex suffix)
	t.Setenv("IMAGE_TAG", "v1.0")
	h.RunCmd(apBin, "deploy", "--cluster=kind-ap-deploy-kind-e2e")
	h.RunCmd("kubectl", "rollout", "status", "deployment/my-app", "-n", ns, "--timeout=60s")

	appliedImage3 := strings.TrimSpace(h.RunCmd("kubectl", "get", "deployment", "my-app", "-n", ns, "-o", "jsonpath={.spec.template.spec.containers[0].image}"))
	expectedImage3 := fmt.Sprintf("%s/my-app:v1.0", imagePrefix)
	if appliedImage3 != expectedImage3 {
		t.Errorf("expected applied image with explicit tag to be %q, got %q", expectedImage3, appliedImage3)
	}
}
