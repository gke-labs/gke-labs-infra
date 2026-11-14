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
	"testing"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/e2e"
)

func TestKindLoad_PulledMultiPlatform(t *testing.T) {
	if os.Getenv("RUN_E2E") == "" {
		t.Skip("RUN_E2E not set, skipping e2e test")
	}

	h := e2e.NewHarness(t, e2e.Options{ClusterName: "ap-deploy-kind-e2e"})

	image := "alpine:3.22"
	h.RunCmd("docker", "pull", image)

	repoRoot := h.FindRepoRoot()
	apBin := filepath.Join(t.TempDir(), "ap")
	h.RunCmd("go", "build", "-o", apBin, filepath.Join(repoRoot, "ap"))

	h.RunCmd(apBin, "kind-load", "--cluster", "kind://ap-deploy-kind-e2e", image)

	podManifest := fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  name: test-kindload-multiplatform
  namespace: %s
spec:
  restartPolicy: Never
  containers:
  - name: test
    image: %s
    imagePullPolicy: Never
    command: ["/bin/sh", "-c", "sleep 3600"]
`, h.Namespace(), image)

	h.KubectlApplyContent(podManifest)
	h.Wait("pod/test-kindload-multiplatform", "condition=Ready", 60*time.Second)
}
