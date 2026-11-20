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
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"gopkg.in/yaml.v3"
)

// AppliedWorkload records the kind, namespace, and name of an applied workload
// (Deployment, DaemonSet, or StatefulSet).
type AppliedWorkload struct {
	Kind      string
	Namespace string
	Name      string
}

// WorkloadTracker collects applied workloads across manifests.
type WorkloadTracker struct {
	mutex     sync.Mutex
	workloads []AppliedWorkload
	seen      map[string]bool
}

// NewWorkloadTracker creates an empty WorkloadTracker.
func NewWorkloadTracker() *WorkloadTracker {
	return &WorkloadTracker{
		seen: make(map[string]bool),
	}
}

// Add records an applied workload if it is a Deployment, DaemonSet, or StatefulSet.
// Duplicate workloads are ignored.
func (tracker *WorkloadTracker) Add(kind, namespace, name string) {
	if kind != "Deployment" && kind != "DaemonSet" && kind != "StatefulSet" {
		return
	}
	if name == "" {
		return
	}

	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()

	key := fmt.Sprintf("%s/%s/%s", kind, namespace, name)
	if tracker.seen[key] {
		return
	}
	tracker.seen[key] = true
	tracker.workloads = append(tracker.workloads, AppliedWorkload{
		Kind:      kind,
		Namespace: namespace,
		Name:      name,
	})
}

// Workloads returns a snapshot copy of all tracked workloads.
func (tracker *WorkloadTracker) Workloads() []AppliedWorkload {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	copied := make([]AppliedWorkload, len(tracker.workloads))
	copy(copied, tracker.workloads)
	return copied
}

// ExtractWorkloadsFromYAML parses a multi-document YAML string and returns all
// Deployments, DaemonSets, and StatefulSets found within it.
func ExtractWorkloadsFromYAML(content string, fallbackNamespace string) []AppliedWorkload {
	decoder := yaml.NewDecoder(strings.NewReader(content))
	var workloads []AppliedWorkload

	for {
		var document yaml.Node
		if err := decoder.Decode(&document); err != nil {
			break
		}
		if document.Kind != yaml.DocumentNode || len(document.Content) == 0 {
			continue
		}
		root := document.Content[0]
		if root.Kind != yaml.MappingNode {
			continue
		}

		apiVersion, kind := getAPIVersionAndKind(root)
		if kind != "Deployment" && kind != "DaemonSet" && kind != "StatefulSet" {
			continue
		}
		if apiVersion != "" && !strings.HasPrefix(apiVersion, "apps/") && apiVersion != "apps" && !strings.HasPrefix(apiVersion, "extensions/") && apiVersion != "extensions" {
			continue
		}

		name := getMetadataName(root)
		if name == "" {
			continue
		}

		namespace := getMetadataNamespace(root)
		if namespace == "" {
			namespace = fallbackNamespace
		}

		workloads = append(workloads, AppliedWorkload{
			Kind:      kind,
			Namespace: namespace,
			Name:      name,
		})
	}

	return workloads
}

// WaitForWorkloadsTask waits in parallel for all applied Deployments, DaemonSets,
// and StatefulSets to be ready within a single overall timeout.
type WaitForWorkloadsTask struct {
	Cluster   KubeCluster
	Namespace string
	Timeout   time.Duration
	Tracker   *WorkloadTracker
}

// GetName returns the display name for the wait task.
func (task *WaitForWorkloadsTask) GetName() string {
	var parts []string
	if cluster := task.Cluster.String(); cluster != "" {
		parts = append(parts, "context: "+cluster)
	}
	if task.Namespace != "" {
		parts = append(parts, "namespace: "+task.Namespace)
	}
	if len(parts) > 0 {
		return fmt.Sprintf("wait-ready (%s)", strings.Join(parts, ", "))
	}
	return "wait-ready"
}

// GetChildren returns nil because WaitForWorkloadsTask executes its workloads concurrently.
func (task *WaitForWorkloadsTask) GetChildren() []tasks.Task {
	return nil
}

type workloadResult struct {
	workload AppliedWorkload
	err      error
	output   string
}

// Run executes the parallel wait for all workloads in Tracker.
func (task *WaitForWorkloadsTask) Run(ctx context.Context, scope *tasks.TaskContext) error {
	workloads := task.Tracker.Workloads()
	if len(workloads) == 0 {
		return nil
	}

	kubectlBinary, err := scope.Tool(ctx, "kubectl")
	if err != nil {
		return fmt.Errorf("failed to ensure kubectl: %w", err)
	}

	scoped.Log(ctx).Info("waiting for workloads to be ready", "count", len(workloads), "timeout", task.Timeout)

	waitContext, cancel := context.WithTimeout(ctx, task.Timeout)
	defer cancel()

	results := make([]workloadResult, len(workloads))
	var waitGroup sync.WaitGroup

	for index, workload := range workloads {
		waitGroup.Add(1)
		go func(i int, item AppliedWorkload) {
			defer waitGroup.Done()

			var args []string
			args = append(args, task.Cluster.KubectlArgs()...)
			resourceArgument := strings.ToLower(item.Kind) + "/" + item.Name
			args = append(args, "rollout", "status", resourceArgument)
			if item.Namespace != "" {
				args = append(args, "-n", item.Namespace)
			}
			args = append(args, fmt.Sprintf("--timeout=%s", task.Timeout.Round(time.Second).String()))

			command := exec.CommandContext(waitContext, kubectlBinary, args...)
			output, commandErr := command.CombinedOutput()

			results[i] = workloadResult{
				workload: item,
				err:      commandErr,
				output:   string(output),
			}

			if commandErr == nil {
				scoped.Log(ctx).Info("rollout complete", "workload", resourceArgument)
			}
		}(index, workload)
	}

	waitGroup.Wait()

	var failures []workloadResult
	for _, result := range results {
		if result.err != nil {
			failures = append(failures, result)
		}
	}

	if len(failures) == 0 {
		return nil
	}

	diagnosticContext, diagnosticCancel := context.WithTimeout(ctx, 30*time.Second)
	defer diagnosticCancel()

	var failureMessages []string
	for _, failure := range failures {
		diagnostics := task.formatDiagnostics(diagnosticContext, kubectlBinary, failure)
		failureMessages = append(failureMessages, diagnostics)
	}

	return fmt.Errorf("workloads failed to become ready within %s:\n%s", task.Timeout, strings.Join(failureMessages, "\n"))
}

func (task *WaitForWorkloadsTask) formatDiagnostics(ctx context.Context, kubectlBinary string, failure workloadResult) string {
	item := failure.workload
	workloadDescription := fmt.Sprintf("%s/%s", strings.ToLower(item.Kind), item.Name)
	if item.Namespace != "" {
		workloadDescription = fmt.Sprintf("%s/%s in namespace %s", strings.ToLower(item.Kind), item.Name, item.Namespace)
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("- %s: rollout failed", workloadDescription))

	rolloutOutput := strings.TrimSpace(failure.output)
	if rolloutOutput != "" {
		lines = append(lines, fmt.Sprintf("    %s", rolloutOutput))
	}

	var selectorArgs []string
	selectorArgs = append(selectorArgs, task.Cluster.KubectlArgs()...)
	if item.Namespace != "" {
		selectorArgs = append(selectorArgs, "-n", item.Namespace)
	}
	selectorArgs = append(selectorArgs, "get", strings.ToLower(item.Kind), item.Name, "-o", "go-template={{range $k, $v := .spec.selector.matchLabels}}{{$k}}={{$v}},{{end}}")

	selectorCommand := exec.CommandContext(ctx, kubectlBinary, selectorArgs...)
	selectorOutput, err := selectorCommand.Output()
	if err != nil {
		scoped.Log(ctx).Fine("failed to query workload selector for diagnostics", "workload", item.Name, "error", err)
		return strings.Join(lines, "\n")
	}

	selector := strings.TrimSuffix(strings.TrimSpace(string(selectorOutput)), ",")
	if selector != "" {
		var podArgs []string
		podArgs = append(podArgs, task.Cluster.KubectlArgs()...)
		if item.Namespace != "" {
			podArgs = append(podArgs, "-n", item.Namespace)
		}
		podArgs = append(podArgs, "get", "pods", "-l", selector, "-o", "custom-columns=NAME:.metadata.name,PHASE:.status.phase,WAITING:.status.containerStatuses[*].state.waiting.reason,TERMINATION:.status.containerStatuses[*].lastState.terminated.message")

		podCommand := exec.CommandContext(ctx, kubectlBinary, podArgs...)
		podOutput, podErr := podCommand.CombinedOutput()
		if podErr != nil {
			scoped.Log(ctx).Fine("failed to query pod details for diagnostics", "workload", item.Name, "error", podErr)
		} else {
			podText := strings.TrimSpace(string(podOutput))
			if podText != "" {
				for _, podLine := range strings.Split(podText, "\n") {
					lines = append(lines, fmt.Sprintf("    %s", podLine))
				}
			}
		}
	}

	return strings.Join(lines, "\n")
}
