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

package k8s

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

func TestWaitForWorkloadsTask_AllReadyReturnsNil(t *testing.T) {
	tempDir := t.TempDir()
	fakeKubectl := filepath.Join(tempDir, "kubectl")
	fakeScript := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(fakeKubectl, []byte(fakeScript), 0755); err != nil {
		t.Fatal(err)
	}

	tree, err := sourcetree.Open(tempDir)
	if err != nil {
		t.Fatal(err)
	}
	scope := tasks.NewTaskContext(tempDir, tempDir, tree).WithTool("kubectl", fakeKubectl)

	tracker := NewWorkloadTracker()
	tracker.Add("Deployment", "default", "my-app")
	tracker.Add("DaemonSet", "kube-system", "my-agent")

	task := &WaitForWorkloadsTask{
		Timeout: 5 * time.Second,
		Tracker: tracker,
	}

	if err := task.Run(t.Context(), scope); err != nil {
		t.Fatalf("expected nil error when all workloads are ready, got: %v", err)
	}
}

func TestWaitForWorkloadsTask_NotReadyFailsWithDiagnostics(t *testing.T) {
	tempDir := t.TempDir()
	fakeKubectl := filepath.Join(tempDir, "kubectl")
	fakeScript := `#!/bin/sh
command="$*"
case "$command" in
  *"rollout status"*)
    echo 'Waiting for deployment "web" rollout to finish: 0 of 1 updated replicas are available...'
    echo 'error: timed out waiting for the condition'
    exit 1
    ;;
  *"get deployment web -o go-template"*)
    echo "app=web,"
    exit 0
    ;;
  *"get pods -l app=web"*)
    echo "NAME        PHASE    WAITING           TERMINATION"
    echo "web-abc123  Running  CrashLoopBackOff  <none>"
    exit 0
    ;;
esac
exit 0
`
	if err := os.WriteFile(fakeKubectl, []byte(fakeScript), 0755); err != nil {
		t.Fatal(err)
	}

	tree, err := sourcetree.Open(tempDir)
	if err != nil {
		t.Fatal(err)
	}
	scope := tasks.NewTaskContext(tempDir, tempDir, tree).WithTool("kubectl", fakeKubectl)

	tracker := NewWorkloadTracker()
	tracker.Add("Deployment", "prod", "web")

	task := &WaitForWorkloadsTask{
		Timeout: 5 * time.Second,
		Tracker: tracker,
	}

	err = task.Run(t.Context(), scope)
	if err == nil {
		t.Fatalf("expected error when workload is not ready, got nil")
	}

	errText := err.Error()
	if !strings.Contains(errText, "deployment/web in namespace prod") {
		t.Errorf("expected error to name deployment/web in namespace prod, got:\n%s", errText)
	}
	if !strings.Contains(errText, "Waiting for deployment \"web\" rollout to finish: 0 of 1 updated replicas are available...") {
		t.Errorf("expected error to include rollout status replica text, got:\n%s", errText)
	}
	if !strings.Contains(errText, "CrashLoopBackOff") {
		t.Errorf("expected error to include pod CrashLoopBackOff text, got:\n%s", errText)
	}
}
