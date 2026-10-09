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
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
)

const (
	// PoolLabel marks sandboxes and secrets as belonging to a runner pool.
	PoolLabel = "test-runner.gke-labs.dev/pool"
	// RunnerIDAnnotation records the GitHub runner ID registered for a sandbox.
	RunnerIDAnnotation = "test-runner.gke-labs.dev/github-runner-id"
	// ComponentLabel identifies pods created by this controller.
	ComponentLabel = "app.kubernetes.io/component"
	// ComponentValue is the value of ComponentLabel.
	ComponentValue = "github-runner"

	// jitConfigKey is the key in the per-runner Secret that holds the JIT config.
	jitConfigKey = "jitconfig"
	// jitConfigEnv is the environment variable the actions runner reads its
	// JIT configuration from (equivalent to run.sh --jitconfig).
	jitConfigEnv = "ACTIONS_RUNNER_INPUT_JITCONFIG"

	// runnerUID is the uid of the "runner" user in ghcr.io/actions/actions-runner.
	runnerUID = 1001
	// runnerHome is the home directory of the runner user in the image.
	runnerHome = "/home/runner"
)

// RunnerSpec describes how runner sandboxes are built.
type RunnerSpec struct {
	// Image is the GitHub Actions runner image.
	Image string
	// RuntimeClassName, if set, is the RuntimeClass used for the runner pod.
	// Empty means the cluster default (plain runc / cgroups isolation).
	RuntimeClassName string
	// MaxLifetime bounds how long a sandbox may exist. agent-sandbox tears
	// the sandbox down when it is reached, even if this controller is down.
	MaxLifetime time.Duration
	// Resources are the resource requests/limits of the runner container.
	Resources corev1.ResourceRequirements
	// WorkVolumeSizeLimit bounds the emptyDir used for the runner work directory.
	WorkVolumeSizeLimit resource.Quantity
	// Env is extra environment for the runner container.
	Env []corev1.EnvVar
}

// buildSandbox returns the Sandbox for a single ephemeral runner. The
// JIT configuration is read from the Secret of the same name.
func buildSandbox(pool, name string, spec RunnerSpec, now time.Time) *sandboxv1beta1.Sandbox {
	labels := map[string]string{
		PoolLabel:      pool,
		ComponentLabel: ComponentValue,
	}

	env := []corev1.EnvVar{
		{
			Name: jitConfigEnv,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: name},
					Key:                  jitConfigKey,
				},
			},
		},
		{Name: "HOME", Value: runnerHome},
	}
	env = append(env, spec.Env...)

	podSpec := corev1.PodSpec{
		// The runner must never be able to reach the Kubernetes API.
		AutomountServiceAccountToken: ptr.To(false),
		EnableServiceLinks:           ptr.To(false),
		// A JIT runner exits after one job; the Sandbox then reports
		// Finished and the pool replaces it.
		RestartPolicy:                 corev1.RestartPolicyNever,
		TerminationGracePeriodSeconds: ptr.To[int64](60),
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot:   ptr.To(true),
			RunAsUser:      ptr.To[int64](runnerUID),
			RunAsGroup:     ptr.To[int64](runnerUID),
			FSGroup:        ptr.To[int64](runnerUID),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		Containers: []corev1.Container{{
			Name:    "runner",
			Image:   spec.Image,
			Command: []string{runnerHome + "/run.sh"},
			Env:     env,
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: ptr.To(false),
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			},
			Resources: spec.Resources,
			VolumeMounts: []corev1.VolumeMount{
				{Name: "work", MountPath: runnerHome + "/_work"},
				{Name: "tmp", MountPath: "/tmp"},
			},
		}},
		Volumes: []corev1.Volume{
			{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: sizeLimit(spec.WorkVolumeSizeLimit)}}},
			{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		},
	}
	if spec.RuntimeClassName != "" {
		podSpec.RuntimeClassName = ptr.To(spec.RuntimeClassName)
	}

	sb := &sandboxv1beta1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: sandboxv1beta1.SandboxSpec{
			SandboxBlueprint: sandboxv1beta1.SandboxBlueprint{
				PodTemplate: sandboxv1beta1.PodTemplate{
					ObjectMeta: sandboxv1beta1.PodMetadata{Labels: labels},
					Spec:       podSpec,
				},
				Service: ptr.To(false),
			},
			OperatingMode: sandboxv1beta1.SandboxOperatingModeRunning,
		},
	}
	if spec.MaxLifetime > 0 {
		sb.Spec.Lifecycle = sandboxv1beta1.Lifecycle{
			ShutdownTime:   ptr.To(metav1.NewTime(now.Add(spec.MaxLifetime))),
			ShutdownPolicy: ptr.To(sandboxv1beta1.ShutdownPolicyDelete),
		}
	}
	return sb
}

func sizeLimit(q resource.Quantity) *resource.Quantity {
	if q.IsZero() {
		return nil
	}
	return &q
}

// buildSecret returns the Secret holding the JIT config for a runner.
func buildSecret(pool, name, jitConfig string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				PoolLabel:      pool,
				ComponentLabel: ComponentValue,
			},
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{jitConfigKey: jitConfig},
	}
}

// sandboxOwnerRef returns an owner reference pointing at a created sandbox.
func sandboxOwnerRef(sb *sandboxv1beta1.Sandbox) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: sandboxv1beta1.GroupVersion.String(),
		Kind:       "Sandbox",
		Name:       sb.Name,
		UID:        sb.UID,
		Controller: ptr.To(true),
	}
}

// isFinished reports whether a sandbox's runner has terminated (for any
// reason) and the sandbox should be replaced.
func isFinished(sb *sandboxv1beta1.Sandbox) bool {
	for _, c := range sb.Status.Conditions {
		switch c.Type {
		case string(sandboxv1beta1.SandboxConditionFinished):
			if c.Status == metav1.ConditionTrue {
				return true
			}
		case string(sandboxv1beta1.SandboxConditionReady):
			if c.Status != metav1.ConditionFalse {
				continue
			}
			switch c.Reason {
			case sandboxv1beta1.SandboxReasonPodSucceeded,
				sandboxv1beta1.SandboxReasonPodFailed,
				sandboxv1beta1.SandboxReasonExpired,
				sandboxv1beta1.SandboxReasonInvalidConfiguration:
				return true
			}
		}
	}
	return false
}
