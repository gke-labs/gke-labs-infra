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
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

type mockTask struct {
	called bool
}

func (m *mockTask) Run(ctx context.Context, scope *tasks.TaskContext) error {
	m.called = true
	return nil
}

func (m *mockTask) GetName() string {
	return "mock-task"
}

func (m *mockTask) GetChildren() []tasks.Task {
	return nil
}

func TestPortForwardTask_Properties(t *testing.T) {
	child := &mockTask{}
	pf := &PortForwardTask{
		Child:      child,
		Service:    "test-service",
		Namespace:  "test-ns",
		LocalPort:  5000,
		RemotePort: 80,
	}

	if pf.GetName() != "port-forward-mock-task" {
		t.Errorf("expected name port-forward-mock-task, got %s", pf.GetName())
	}

	children := pf.GetChildren()
	if len(children) != 1 || children[0] != child {
		t.Errorf("expected 1 child which is mockTask, got %v", children)
	}
}

func TestIsInCluster(t *testing.T) {
	// Backup env and var
	origEnv := os.Getenv("KUBERNETES_SERVICE_HOST")
	defer os.Setenv("KUBERNETES_SERVICE_HOST", origEnv)

	origTokenPath := serviceAccountTokenPath
	defer func() { serviceAccountTokenPath = origTokenPath }()

	os.Setenv("KUBERNETES_SERVICE_HOST", "")
	if IsInCluster() {
		t.Error("IsInCluster should be false when KUBERNETES_SERVICE_HOST is empty")
	}

	os.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	serviceAccountTokenPath = filepath.Join(t.TempDir(), "missing-token")
	if IsInCluster() {
		t.Error("IsInCluster should be false when service account token does not exist")
	}

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("fake-token"), 0600); err != nil {
		t.Fatal(err)
	}
	serviceAccountTokenPath = tokenFile
	if !IsInCluster() {
		t.Error("IsInCluster should be true when service account token exists")
	}
}

func TestPickFreePort(t *testing.T) {
	port, err := pickFreePort()
	if err != nil {
		t.Fatalf("pickFreePort() error = %v", err)
	}
	if port <= 0 || port > 65535 {
		t.Errorf("pickFreePort() returned invalid port = %d", port)
	}
}
