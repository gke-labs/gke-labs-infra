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
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestExtractWorkloadsFromYAML(t *testing.T) {
	yamlContent := `
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-config
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: app-deploy
  namespace: custom-ns
spec:
  replicas: 2
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: node-agent
spec: {}
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: database
  namespace: db-ns
spec:
  replicas: 3
---
apiVersion: batch/v1
kind: Job
metadata:
  name: batch-job
spec: {}
`
	workloads := ExtractWorkloadsFromYAML(yamlContent, "fallback-ns")

	expected := []AppliedWorkload{
		{Kind: "Deployment", Namespace: "custom-ns", Name: "app-deploy"},
		{Kind: "DaemonSet", Namespace: "fallback-ns", Name: "node-agent"},
		{Kind: "StatefulSet", Namespace: "db-ns", Name: "database"},
	}

	if !reflect.DeepEqual(workloads, expected) {
		t.Fatalf("ExtractWorkloadsFromYAML = %+v, want %+v", workloads, expected)
	}
}

func TestWorkloadTracker_DeduplicationAndFiltering(t *testing.T) {
	tracker := NewWorkloadTracker()

	// Valid workloads
	tracker.Add("Deployment", "ns1", "app")
	tracker.Add("Deployment", "ns1", "app") // duplicate
	tracker.Add("DaemonSet", "ns1", "agent")
	tracker.Add("StatefulSet", "ns2", "db")

	// Invalid / ignored kinds
	tracker.Add("ConfigMap", "ns1", "cm")
	tracker.Add("Service", "ns1", "svc")
	tracker.Add("Deployment", "ns1", "") // empty name

	workloads := tracker.Workloads()
	if len(workloads) != 3 {
		t.Fatalf("expected 3 workloads, got %d: %+v", len(workloads), workloads)
	}

	expected := []AppliedWorkload{
		{Kind: "Deployment", Namespace: "ns1", Name: "app"},
		{Kind: "DaemonSet", Namespace: "ns1", Name: "agent"},
		{Kind: "StatefulSet", Namespace: "ns2", Name: "db"},
	}
	if !reflect.DeepEqual(workloads, expected) {
		t.Fatalf("Workloads() = %+v, want %+v", workloads, expected)
	}
}

func TestWorkloadTracker_ConcurrentSafety(t *testing.T) {
	tracker := NewWorkloadTracker()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := fmt.Sprintf("app-%d", idx%5)
			tracker.Add("Deployment", "default", name)
		}(i)
	}

	wg.Wait()
	workloads := tracker.Workloads()
	if len(workloads) != 5 {
		t.Fatalf("expected 5 unique workloads after concurrent adds, got %d", len(workloads))
	}
}

func TestWaitForWorkloadsTask_Empty(t *testing.T) {
	task := &WaitForWorkloadsTask{
		Tracker: NewWorkloadTracker(),
	}

	executed := false
	task.ExecCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		executed = true
		return nil, nil
	}

	if err := task.Run(t.Context(), nil); err != nil {
		t.Fatalf("unexpected error on empty tracker: %v", err)
	}
	if executed {
		t.Fatalf("expected no kubectl calls when tracker is empty")
	}
}

func TestWaitForWorkloadsTask_Success(t *testing.T) {
	tracker := NewWorkloadTracker()
	tracker.Add("Deployment", "ns1", "app1")
	tracker.Add("DaemonSet", "ns2", "agent1")

	task := &WaitForWorkloadsTask{
		Cluster: ClusterFromContext("test-ctx"),
		Timeout: 2 * time.Minute,
		Tracker: tracker,
	}

	var mu sync.Mutex
	var commands []string

	task.ExecCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		commands = append(commands, strings.Join(args, " "))
		return []byte("rollout status ok"), nil
	}

	if err := task.Run(t.Context(), nil); err != nil {
		t.Fatalf("task.Run failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(commands) != 2 {
		t.Fatalf("expected 2 rollout status commands, got %d: %v", len(commands), commands)
	}

	hasApp1 := false
	hasAgent1 := false
	for _, cmd := range commands {
		if strings.Contains(cmd, "rollout status deployment/app1") && strings.Contains(cmd, "-n ns1") && strings.Contains(cmd, "--context test-ctx") {
			hasApp1 = true
		}
		if strings.Contains(cmd, "rollout status daemonset/agent1") && strings.Contains(cmd, "-n ns2") && strings.Contains(cmd, "--context test-ctx") {
			hasAgent1 = true
		}
	}

	if !hasApp1 {
		t.Errorf("missing expected rollout status for deployment/app1 in commands: %v", commands)
	}
	if !hasAgent1 {
		t.Errorf("missing expected rollout status for daemonset/agent1 in commands: %v", commands)
	}
}

func TestWaitForWorkloadsTask_Failure_UnavailableReplicas(t *testing.T) {
	tracker := NewWorkloadTracker()
	tracker.Add("Deployment", "prod", "web")

	task := &WaitForWorkloadsTask{
		Timeout: 30 * time.Second,
		Tracker: tracker,
	}

	task.ExecCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmdStr := strings.Join(args, " ")
		if strings.Contains(cmdStr, "rollout status") {
			return []byte("error: timed out waiting for the condition"), fmt.Errorf("exit status 1")
		}
		if strings.Contains(cmdStr, "get deployment web") {
			deploymentJSON := `{
				"spec": {"replicas": 3, "selector": {"matchLabels": {"app": "web"}}},
				"status": {
					"replicas": 3,
					"readyReplicas": 1,
					"availableReplicas": 1,
					"unavailableReplicas": 2,
					"updatedReplicas": 3
				}
			}`
			return []byte(deploymentJSON), nil
		}
		if strings.Contains(cmdStr, "get pods -l app=web") {
			return []byte(`{"items": []}`), nil
		}
		return nil, nil
	}

	err := task.Run(t.Context(), nil)
	if err == nil {
		t.Fatalf("expected error on failed rollout, got nil")
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "deployment/web in namespace prod: 1/3 replicas available (2 unavailable)") {
		t.Errorf("expected error to report unavailable replicas, got:\n%s", errStr)
	}
}

func TestWaitForWorkloadsTask_Failure_PodWaitingAndTerminated(t *testing.T) {
	tracker := NewWorkloadTracker()
	tracker.Add("Deployment", "default", "api")

	task := &WaitForWorkloadsTask{
		Timeout: 10 * time.Second,
		Tracker: tracker,
	}

	task.ExecCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmdStr := strings.Join(args, " ")
		if strings.Contains(cmdStr, "rollout status") {
			return []byte("error: timed out waiting for the condition"), fmt.Errorf("timed out")
		}
		if strings.Contains(cmdStr, "get deployment api") {
			deploymentJSON := `{
				"spec": {"replicas": 1, "selector": {"matchLabels": {"app": "api"}}},
				"status": {"replicas": 1, "availableReplicas": 0, "unavailableReplicas": 1}
			}`
			return []byte(deploymentJSON), nil
		}
		if strings.Contains(cmdStr, "get pods -l app=api") {
			podsJSON := `{
				"items": [{
					"metadata": {"name": "api-7f89d-abc"},
					"status": {
						"phase": "Running",
						"conditions": [{"type": "Ready", "status": "False"}],
						"containerStatuses": [{
							"name": "server",
							"ready": false,
							"state": {
								"waiting": {
									"reason": "CrashLoopBackOff",
									"message": "back-off 10s restarting failed container"
								}
							},
							"lastState": {
								"terminated": {
									"reason": "Error",
									"exitCode": 1,
									"message": "failed to bind port 8080"
								}
							}
						}]
					}
				}]
			}`
			return []byte(podsJSON), nil
		}
		return nil, nil
	}

	err := task.Run(t.Context(), nil)
	if err == nil {
		t.Fatalf("expected error on failed rollout, got nil")
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "deployment/api in namespace default: 0/1 replicas available (1 unavailable)") {
		t.Errorf("expected error to report unavailable replicas, got:\n%s", errStr)
	}
	if !strings.Contains(errStr, `container "server" waiting: CrashLoopBackOff: back-off 10s restarting failed container`) {
		t.Errorf("expected error to report pod waiting reason, got:\n%s", errStr)
	}
	if !strings.Contains(errStr, `container "server" last terminated: Error (exit code 1): failed to bind port 8080`) {
		t.Errorf("expected error to report last termination message, got:\n%s", errStr)
	}
}

func TestWaitForWorkloadsTask_Failure_DaemonSetUnschedulablePod(t *testing.T) {
	tracker := NewWorkloadTracker()
	tracker.Add("DaemonSet", "kube-system", "node-agent")

	task := &WaitForWorkloadsTask{
		Timeout: 10 * time.Second,
		Tracker: tracker,
	}

	task.ExecCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmdStr := strings.Join(args, " ")
		if strings.Contains(cmdStr, "rollout status") {
			return []byte("daemonset not ready"), fmt.Errorf("timeout")
		}
		if strings.Contains(cmdStr, "get daemonset node-agent") {
			dsJSON := `{
				"spec": {"selector": {"matchLabels": {"app": "node-agent"}}},
				"status": {
					"desiredNumberScheduled": 3,
					"numberReady": 0,
					"numberUnavailable": 3
				}
			}`
			return []byte(dsJSON), nil
		}
		if strings.Contains(cmdStr, "get pods -l app=node-agent") {
			podsJSON := `{
				"items": [{
					"metadata": {"name": "node-agent-xyz"},
					"status": {
						"phase": "Pending",
						"conditions": [{
							"type": "PodScheduled",
							"status": "False",
							"reason": "Unschedulable",
							"message": "0/1 nodes are available: 1 Insufficient cpu"
						}]
					}
				}]
			}`
			return []byte(podsJSON), nil
		}
		return nil, nil
	}

	err := task.Run(t.Context(), nil)
	if err == nil {
		t.Fatalf("expected error on failed rollout, got nil")
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "daemonset/node-agent in namespace kube-system: 0/3 ready (3 unavailable)") {
		t.Errorf("expected error to report daemonset replica counts, got:\n%s", errStr)
	}
	if !strings.Contains(errStr, "PodScheduled=False (Unschedulable): 0/1 nodes are available: 1 Insufficient cpu") {
		t.Errorf("expected error to report unschedulable pod issue, got:\n%s", errStr)
	}
}

func TestWaitForWorkloadsTask_Failure_InitContainer(t *testing.T) {
	tracker := NewWorkloadTracker()
	tracker.Add("Deployment", "default", "web")

	task := &WaitForWorkloadsTask{
		Timeout: 5 * time.Second,
		Tracker: tracker,
	}

	task.ExecCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmdStr := strings.Join(args, " ")
		if strings.Contains(cmdStr, "rollout status") {
			return []byte("error: timed out"), fmt.Errorf("timed out")
		}
		if strings.Contains(cmdStr, "get deployment web") {
			return []byte(`{
				"spec": {"replicas": 1, "selector": {"matchLabels": {"app": "web"}}},
				"status": {"replicas": 1, "availableReplicas": 0, "unavailableReplicas": 1}
			}`), nil
		}
		if strings.Contains(cmdStr, "get pods -l app=web") {
			return []byte(`{
				"items": [{
					"metadata": {"name": "web-pod-1"},
					"status": {
						"phase": "Pending",
						"initContainerStatuses": [{
							"name": "init-db",
							"ready": false,
							"state": {
								"waiting": {
									"reason": "ImagePullBackOff",
									"message": "Back-off pulling image bad-repo/init:v1"
								}
							}
						}]
					}
				}]
			}`), nil
		}
		return nil, nil
	}

	err := task.Run(t.Context(), nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	errStr := err.Error()
	if !strings.Contains(errStr, `init container "init-db" waiting: ImagePullBackOff: Back-off pulling image bad-repo/init:v1`) {
		t.Errorf("expected error to mention init container, got:\n%s", errStr)
	}
}
