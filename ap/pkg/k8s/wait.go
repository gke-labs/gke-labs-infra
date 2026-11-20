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
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sort"
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
	mu        sync.Mutex
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
func (wt *WorkloadTracker) Add(kind, namespace, name string) {
	if kind != "Deployment" && kind != "DaemonSet" && kind != "StatefulSet" {
		return
	}
	if name == "" {
		return
	}

	wt.mu.Lock()
	defer wt.mu.Unlock()

	if wt.seen == nil {
		wt.seen = make(map[string]bool)
	}
	key := fmt.Sprintf("%s/%s/%s", kind, namespace, name)
	if wt.seen[key] {
		return
	}
	wt.seen[key] = true
	wt.workloads = append(wt.workloads, AppliedWorkload{
		Kind:      kind,
		Namespace: namespace,
		Name:      name,
	})
}

// Workloads returns a snapshot copy of all tracked workloads.
func (wt *WorkloadTracker) Workloads() []AppliedWorkload {
	if wt == nil {
		return nil
	}
	wt.mu.Lock()
	defer wt.mu.Unlock()
	res := make([]AppliedWorkload, len(wt.workloads))
	copy(res, wt.workloads)
	return res
}

// ExtractWorkloadsFromYAML parses a multi-document YAML string and returns all
// Deployments, DaemonSets, and StatefulSets found within it.
func ExtractWorkloadsFromYAML(content string, fallbackNamespace string) []AppliedWorkload {
	decoder := yaml.NewDecoder(strings.NewReader(content))
	var workloads []AppliedWorkload

	for {
		var doc yaml.Node
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
			continue
		}
		root := doc.Content[0]
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

		ns := getMetadataNamespace(root)
		if ns == "" {
			ns = fallbackNamespace
		}

		workloads = append(workloads, AppliedWorkload{
			Kind:      kind,
			Namespace: ns,
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

	// ExecCmd can be overridden for testing.
	ExecCmd func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// GetName returns the display name for the wait task.
func (t *WaitForWorkloadsTask) GetName() string {
	var parts []string
	if cluster := t.Cluster.String(); cluster != "" {
		parts = append(parts, "context: "+cluster)
	}
	if t.Namespace != "" {
		parts = append(parts, "namespace: "+t.Namespace)
	}
	if len(parts) > 0 {
		return fmt.Sprintf("wait-ready (%s)", strings.Join(parts, ", "))
	}
	return "wait-ready"
}

// GetChildren returns nil because WaitForWorkloadsTask executes its workloads concurrently.
func (t *WaitForWorkloadsTask) GetChildren() []tasks.Task {
	return nil
}

type workloadRunResult struct {
	workload AppliedWorkload
	err      error
	output   string
}

// Run executes the parallel wait for all workloads in Tracker.
func (t *WaitForWorkloadsTask) Run(ctx context.Context, scope *tasks.TaskContext) error {
	var workloads []AppliedWorkload
	if t.Tracker != nil {
		workloads = t.Tracker.Workloads()
	}
	if len(workloads) == 0 {
		return nil
	}

	var kubectlBin string
	if scope != nil {
		var err error
		kubectlBin, err = scope.Tool(ctx, "kubectl")
		if err != nil {
			return fmt.Errorf("failed to ensure kubectl: %w", err)
		}
	} else if t.ExecCmd == nil {
		kubectlBin = "kubectl"
	}

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	scoped.Log(ctx).Info("waiting for workloads to be ready", "count", len(workloads), "timeout", timeout)

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	results := make([]workloadRunResult, len(workloads))
	var wg sync.WaitGroup

	for i, w := range workloads {
		wg.Add(1)
		go func(index int, wl AppliedWorkload) {
			defer wg.Done()

			var args []string
			args = append(args, t.Cluster.KubectlArgs()...)
			resourceArg := strings.ToLower(wl.Kind) + "/" + wl.Name
			args = append(args, "rollout", "status", resourceArg)
			if wl.Namespace != "" {
				args = append(args, "-n", wl.Namespace)
			}
			args = append(args, fmt.Sprintf("--timeout=%s", timeout.Round(time.Second).String()))

			var out []byte
			var err error
			if t.ExecCmd != nil {
				out, err = t.ExecCmd(waitCtx, kubectlBin, args...)
			} else {
				cmd := exec.CommandContext(waitCtx, kubectlBin, args...)
				out, err = cmd.CombinedOutput()
			}

			results[index] = workloadRunResult{
				workload: wl,
				err:      err,
				output:   string(out),
			}

			if err == nil {
				scoped.Log(ctx).Info("rollout complete", "workload", resourceArg)
			}
		}(i, w)
	}

	wg.Wait()

	var failures []workloadRunResult
	for _, res := range results {
		if res.err != nil {
			failures = append(failures, res)
		}
	}

	if len(failures) == 0 {
		return nil
	}

	// Use parent ctx with a fresh timeout to collect diagnostics for failed workloads.
	diagCtx, diagCancel := context.WithTimeout(ctx, 30*time.Second)
	defer diagCancel()

	var failureMsgs []string
	for _, f := range failures {
		diag := t.getDiagnostics(diagCtx, kubectlBin, f.workload, f.err, f.output)
		failureMsgs = append(failureMsgs, diag)
	}

	return fmt.Errorf("workloads failed to become ready within %s:\n%s", timeout, strings.Join(failureMsgs, "\n"))
}

func (t *WaitForWorkloadsTask) execKubectl(ctx context.Context, kubectlBin string, args []string) ([]byte, error) {
	if t.ExecCmd != nil {
		return t.ExecCmd(ctx, kubectlBin, args...)
	}
	cmd := exec.CommandContext(ctx, kubectlBin, args...)
	return cmd.Output()
}

func (t *WaitForWorkloadsTask) getDiagnostics(ctx context.Context, kubectlBin string, wl AppliedWorkload, rolloutErr error, rolloutOutput string) string {
	workloadDesc := fmt.Sprintf("%s/%s", strings.ToLower(wl.Kind), wl.Name)
	if wl.Namespace != "" {
		workloadDesc = fmt.Sprintf("%s/%s in namespace %s", strings.ToLower(wl.Kind), wl.Name, wl.Namespace)
	}

	var getArgs []string
	getArgs = append(getArgs, t.Cluster.KubectlArgs()...)
	if wl.Namespace != "" {
		getArgs = append(getArgs, "-n", wl.Namespace)
	}
	getArgs = append(getArgs, "get", strings.ToLower(wl.Kind), wl.Name, "-o", "json")

	getOut, getErr := t.execKubectl(ctx, kubectlBin, getArgs)
	if getErr != nil {
		trimmed := strings.TrimSpace(rolloutOutput)
		if trimmed != "" {
			return fmt.Sprintf("- %s: rollout failed: %s", workloadDesc, trimmed)
		}
		return fmt.Sprintf("- %s: rollout failed: %v", workloadDesc, rolloutErr)
	}

	var obj struct {
		Spec struct {
			Replicas *int32 `json:"replicas"`
			Selector *struct {
				MatchLabels map[string]string `json:"matchLabels"`
			} `json:"selector"`
		} `json:"spec"`
		Status struct {
			Replicas               int32 `json:"replicas"`
			ReadyReplicas          int32 `json:"readyReplicas"`
			AvailableReplicas      int32 `json:"availableReplicas"`
			UnavailableReplicas    int32 `json:"unavailableReplicas"`
			UpdatedReplicas        int32 `json:"updatedReplicas"`
			DesiredNumberScheduled int32 `json:"desiredNumberScheduled"`
			CurrentNumberScheduled int32 `json:"currentNumberScheduled"`
			NumberReady            int32 `json:"numberReady"`
			NumberAvailable        int32 `json:"numberAvailable"`
			NumberUnavailable      int32 `json:"numberUnavailable"`
			Conditions             []struct {
				Type    string `json:"type"`
				Status  string `json:"status"`
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"conditions"`
		} `json:"status"`
	}

	if err := json.Unmarshal(getOut, &obj); err != nil {
		trimmed := strings.TrimSpace(rolloutOutput)
		if trimmed != "" {
			return fmt.Sprintf("- %s: rollout failed: %s", workloadDesc, trimmed)
		}
		return fmt.Sprintf("- %s: rollout failed: %v", workloadDesc, rolloutErr)
	}

	var replicaSummary string
	switch wl.Kind {
	case "Deployment":
		desired := int32(1)
		if obj.Spec.Replicas != nil {
			desired = *obj.Spec.Replicas
		}
		if obj.Status.UnavailableReplicas > 0 {
			replicaSummary = fmt.Sprintf("%d/%d replicas available (%d unavailable)", obj.Status.AvailableReplicas, desired, obj.Status.UnavailableReplicas)
		} else {
			replicaSummary = fmt.Sprintf("%d/%d replicas available", obj.Status.AvailableReplicas, desired)
		}
	case "DaemonSet":
		if obj.Status.NumberUnavailable > 0 {
			replicaSummary = fmt.Sprintf("%d/%d ready (%d unavailable)", obj.Status.NumberReady, obj.Status.DesiredNumberScheduled, obj.Status.NumberUnavailable)
		} else {
			replicaSummary = fmt.Sprintf("%d/%d ready", obj.Status.NumberReady, obj.Status.DesiredNumberScheduled)
		}
	case "StatefulSet":
		desired := int32(1)
		if obj.Spec.Replicas != nil {
			desired = *obj.Spec.Replicas
		}
		replicaSummary = fmt.Sprintf("%d/%d ready", obj.Status.ReadyReplicas, desired)
	default:
		replicaSummary = "not ready"
	}

	var conditionMessages []string
	for _, cond := range obj.Status.Conditions {
		if cond.Type == "ReplicaFailure" || (cond.Type == "Progressing" && cond.Status == "False") || (cond.Type == "Available" && cond.Status == "False") {
			conditionMessages = append(conditionMessages, fmt.Sprintf("condition %s=%s (%s): %s", cond.Type, cond.Status, cond.Reason, cond.Message))
		}
	}

	var podMessages []string
	if obj.Spec.Selector != nil && len(obj.Spec.Selector.MatchLabels) > 0 {
		var labelParts []string
		for k, v := range obj.Spec.Selector.MatchLabels {
			labelParts = append(labelParts, fmt.Sprintf("%s=%s", k, v))
		}
		sort.Strings(labelParts)
		selectorStr := strings.Join(labelParts, ",")

		var podArgs []string
		podArgs = append(podArgs, t.Cluster.KubectlArgs()...)
		if wl.Namespace != "" {
			podArgs = append(podArgs, "-n", wl.Namespace)
		}
		podArgs = append(podArgs, "get", "pods", "-l", selectorStr, "-o", "json")

		podOut, podErr := t.execKubectl(ctx, kubectlBin, podArgs)
		if podErr != nil {
			scoped.Log(ctx).Fine("failed to get pod details for workload diagnostics", "workload", wl.Name, "error", podErr)
		} else {
			podMessages = parseNotReadyPodDetails(podOut)
		}
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("- %s: %s", workloadDesc, replicaSummary))
	for _, condMsg := range conditionMessages {
		lines = append(lines, fmt.Sprintf("    %s", condMsg))
	}
	for _, podMsg := range podMessages {
		lines = append(lines, fmt.Sprintf("    %s", podMsg))
	}
	if len(conditionMessages) == 0 && len(podMessages) == 0 {
		trimmed := strings.TrimSpace(rolloutOutput)
		if trimmed != "" {
			lines = append(lines, fmt.Sprintf("    %s", trimmed))
		}
	}

	return strings.Join(lines, "\n")
}

type containerStatusJSON struct {
	Name         string `json:"name"`
	Ready        bool   `json:"ready"`
	RestartCount int32  `json:"restartCount"`
	State        struct {
		Waiting *struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"waiting"`
		Terminated *struct {
			Reason   string `json:"reason"`
			ExitCode int32  `json:"exitCode"`
			Message  string `json:"message"`
		} `json:"terminated"`
	} `json:"state"`
	LastState struct {
		Terminated *struct {
			Reason   string `json:"reason"`
			ExitCode int32  `json:"exitCode"`
			Message  string `json:"message"`
		} `json:"terminated"`
	} `json:"lastState"`
}

func parseNotReadyPodDetails(podJSON []byte) []string {
	var podList struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Phase      string `json:"phase"`
				Conditions []struct {
					Type    string `json:"type"`
					Status  string `json:"status"`
					Reason  string `json:"reason"`
					Message string `json:"message"`
				} `json:"conditions"`
				InitContainerStatuses []containerStatusJSON `json:"initContainerStatuses"`
				ContainerStatuses     []containerStatusJSON `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}

	if err := json.Unmarshal(podJSON, &podList); err != nil {
		return nil
	}

	var messages []string
	for _, pod := range podList.Items {
		isReady := false
		for _, c := range pod.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				isReady = true
				break
			}
		}
		if isReady {
			continue
		}

		podName := pod.Metadata.Name
		var podIssues []string

		for _, c := range pod.Status.Conditions {
			if c.Type == "PodScheduled" && c.Status == "False" {
				if c.Message != "" {
					podIssues = append(podIssues, fmt.Sprintf("PodScheduled=False (%s): %s", c.Reason, c.Message))
				} else {
					podIssues = append(podIssues, fmt.Sprintf("PodScheduled=False (%s)", c.Reason))
				}
			}
		}

		checkContainers := func(containers []containerStatusJSON, isInit bool) {
			prefix := "container"
			if isInit {
				prefix = "init container"
			}
			for _, cs := range containers {
				if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
					if cs.State.Waiting.Message != "" {
						podIssues = append(podIssues, fmt.Sprintf("%s %q waiting: %s: %s", prefix, cs.Name, cs.State.Waiting.Reason, cs.State.Waiting.Message))
					} else {
						podIssues = append(podIssues, fmt.Sprintf("%s %q waiting: %s", prefix, cs.Name, cs.State.Waiting.Reason))
					}
				}
				if cs.LastState.Terminated != nil && cs.LastState.Terminated.Reason != "" {
					if cs.LastState.Terminated.Message != "" {
						podIssues = append(podIssues, fmt.Sprintf("%s %q last terminated: %s (exit code %d): %s", prefix, cs.Name, cs.LastState.Terminated.Reason, cs.LastState.Terminated.ExitCode, cs.LastState.Terminated.Message))
					} else {
						podIssues = append(podIssues, fmt.Sprintf("%s %q last terminated: %s (exit code %d)", prefix, cs.Name, cs.LastState.Terminated.Reason, cs.LastState.Terminated.ExitCode))
					}
				}
				if cs.State.Terminated != nil && cs.State.Terminated.Reason != "" && cs.LastState.Terminated == nil {
					if cs.State.Terminated.Message != "" {
						podIssues = append(podIssues, fmt.Sprintf("%s %q terminated: %s (exit code %d): %s", prefix, cs.Name, cs.State.Terminated.Reason, cs.State.Terminated.ExitCode, cs.State.Terminated.Message))
					} else {
						podIssues = append(podIssues, fmt.Sprintf("%s %q terminated: %s (exit code %d)", prefix, cs.Name, cs.State.Terminated.Reason, cs.State.Terminated.ExitCode))
					}
				}
			}
		}

		checkContainers(pod.Status.InitContainerStatuses, true)
		checkContainers(pod.Status.ContainerStatuses, false)

		if len(podIssues) == 0 && pod.Status.Phase != "" {
			podIssues = append(podIssues, fmt.Sprintf("status %s", pod.Status.Phase))
		}

		for _, issue := range podIssues {
			messages = append(messages, fmt.Sprintf("pod/%s: %s", podName, issue))
		}
	}

	if len(messages) > 5 {
		extra := len(messages) - 5
		messages = append(messages[:5], fmt.Sprintf("(... and %d more pod issues)", extra))
	}

	return messages
}
