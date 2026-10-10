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

package runnerpool

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
)

func TestBuildSandboxIsLockedDown(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	spec := RunnerSpec{
		Image:               "ghcr.io/actions/actions-runner:test",
		MaxLifetime:         2 * time.Hour,
		WorkVolumeSizeLimit: resource.MustParse("10Gi"),
	}
	sb := buildSandbox("pool", "pool-abc", spec, now)
	pod := sb.Spec.PodTemplate.Spec

	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Fatalf("automountServiceAccountToken must be false, got %v", pod.AutomountServiceAccountToken)
	}
	if pod.ServiceAccountName != "" {
		t.Errorf("runner pods must use the default service account, got %q", pod.ServiceAccountName)
	}
	if pod.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restartPolicy = %q, want Never", pod.RestartPolicy)
	}
	if pod.RuntimeClassName != nil {
		t.Errorf("runtimeClassName should be unset by default, got %q", *pod.RuntimeClassName)
	}
	if pod.SecurityContext == nil || pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot {
		t.Errorf("pod must run as non-root")
	}
	if pod.SecurityContext.SeccompProfile == nil || pod.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("pod must use the RuntimeDefault seccomp profile")
	}
	if len(pod.Containers) != 1 {
		t.Fatalf("expected one container, got %d", len(pod.Containers))
	}
	c := pod.Containers[0]
	if c.Image != spec.Image {
		t.Errorf("image = %q", c.Image)
	}
	if c.SecurityContext == nil || c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation {
		t.Errorf("container must not allow privilege escalation")
	}
	if c.SecurityContext.Capabilities == nil || len(c.SecurityContext.Capabilities.Drop) != 1 || c.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Errorf("container must drop all capabilities, got %+v", c.SecurityContext.Capabilities)
	}
	if c.SecurityContext.Privileged != nil && *c.SecurityContext.Privileged {
		t.Errorf("container must not be privileged")
	}

	var jit *corev1.EnvVar
	for i := range c.Env {
		if c.Env[i].Name == jitConfigEnv {
			jit = &c.Env[i]
		}
	}
	if jit == nil || jit.ValueFrom == nil || jit.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("JIT config must come from a secret, got %+v", jit)
	}
	if jit.ValueFrom.SecretKeyRef.Name != "pool-abc" || jit.ValueFrom.SecretKeyRef.Key != jitConfigKey {
		t.Errorf("JIT secret ref = %+v", jit.ValueFrom.SecretKeyRef)
	}

	if sb.Spec.ShutdownTime == nil || !sb.Spec.ShutdownTime.Time.Equal(now.Add(2*time.Hour)) {
		t.Errorf("shutdownTime = %v, want %v", sb.Spec.ShutdownTime, now.Add(2*time.Hour))
	}
	if sb.Spec.ShutdownPolicy == nil || *sb.Spec.ShutdownPolicy != sandboxv1beta1.ShutdownPolicyDelete {
		t.Errorf("shutdownPolicy = %v, want Delete", sb.Spec.ShutdownPolicy)
	}
	if sb.Spec.Service == nil || *sb.Spec.Service {
		t.Errorf("runner sandboxes must not get a Service")
	}

	var work *corev1.Volume
	for i := range pod.Volumes {
		if pod.Volumes[i].Name == "work" {
			work = &pod.Volumes[i]
		}
	}
	if work == nil || work.EmptyDir == nil || work.EmptyDir.SizeLimit == nil || work.EmptyDir.SizeLimit.Cmp(resource.MustParse("10Gi")) != 0 {
		t.Errorf("work volume should be a size-limited emptyDir, got %+v", work)
	}
}

func TestBuildSandboxRuntimeClass(t *testing.T) {
	spec := RunnerSpec{Image: "img", RuntimeClassName: "gvisor"}
	sb := buildSandbox("pool", "pool-abc", spec, time.Now())
	if got := sb.Spec.PodTemplate.Spec.RuntimeClassName; got == nil || *got != "gvisor" {
		t.Errorf("runtimeClassName = %v, want gvisor", got)
	}
	if sb.Spec.ShutdownTime != nil {
		t.Errorf("no max lifetime should mean no shutdownTime, got %v", sb.Spec.ShutdownTime)
	}
}

func TestIsFinished(t *testing.T) {
	cond := func(typ string, status metav1.ConditionStatus, reason string) metav1.Condition {
		return metav1.Condition{Type: typ, Status: status, Reason: reason}
	}
	ready := string(sandboxv1beta1.SandboxConditionReady)
	finished := string(sandboxv1beta1.SandboxConditionFinished)
	tests := []struct {
		name  string
		conds []metav1.Condition
		want  bool
	}{
		{"no status", nil, false},
		{"starting", []metav1.Condition{cond(ready, metav1.ConditionFalse, sandboxv1beta1.SandboxReasonDependenciesNotReady)}, false},
		{"ready", []metav1.Condition{cond(ready, metav1.ConditionTrue, sandboxv1beta1.SandboxReasonDependenciesReady)}, false},
		{"finished", []metav1.Condition{cond(finished, metav1.ConditionTrue, sandboxv1beta1.SandboxReasonPodSucceeded)}, true},
		{"failed via ready", []metav1.Condition{cond(ready, metav1.ConditionFalse, sandboxv1beta1.SandboxReasonPodFailed)}, true},
		{"expired", []metav1.Condition{cond(ready, metav1.ConditionFalse, sandboxv1beta1.SandboxReasonExpired)}, true},
		{"invalid", []metav1.Condition{cond(ready, metav1.ConditionFalse, sandboxv1beta1.SandboxReasonInvalidConfiguration)}, true},
		{"reconciler error is transient", []metav1.Condition{cond(ready, metav1.ConditionFalse, sandboxv1beta1.SandboxReasonReconcilerError)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sb := &sandboxv1beta1.Sandbox{Status: sandboxv1beta1.SandboxStatus{Conditions: tt.conds}}
			if got := isFinished(sb); got != tt.want {
				t.Errorf("isFinished = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSandboxOwnerRef(t *testing.T) {
	sb := &sandboxv1beta1.Sandbox{}
	sb.Name = "x"
	sb.UID = "u"
	ref := sandboxOwnerRef(sb)
	if ref.APIVersion != "agents.x-k8s.io/v1beta1" || ref.Kind != "Sandbox" || ref.Name != "x" || ref.UID != "u" || ref.Controller == nil || !*ref.Controller {
		t.Errorf("unexpected owner ref %+v", ref)
	}
}
