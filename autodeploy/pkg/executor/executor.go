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

package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/gke-labs/gke-labs-infra/autodeploy/pkg/apis/infra/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	PackageNameLabel      = "infra.labs.gke.io/package-name"
	PackageNamespaceLabel = "infra.labs.gke.io/package-namespace"
	DefaultJobNamespace   = "autodeploy-system"
)

// Runner defines the interface for running 'ap' commands.
type Runner interface {
	DeployFlow(ctx context.Context, pkg *v1alpha1.Package, commit string, args ...string) error
}

// APRunner handles execution of 'ap' commands.
type APRunner struct {
	Client       client.Client
	ImagePrefix  string
	ImageTag     string
	BuildkitHost string
	JobNamespace string
}

// JobName returns the Job name for a given package and commit.
func JobName(packageName, commit string) string {
	name := fmt.Sprintf("deploy-%s-%s", packageName, commit)
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

// DeployFlow runs the full build-test-deploy flow by creating a Kubernetes Job.
func (r *APRunner) DeployFlow(ctx context.Context, pkg *v1alpha1.Package, commit string, args ...string) error {
	image := "images.local/ap-golang:latest"
	if r.ImagePrefix != "" {
		image = fmt.Sprintf("%s/ap-golang:latest", r.ImagePrefix)
	}

	argStr := strings.Join(args, " ")
	deployArgs := "-n " + pkg.Namespace
	if argStr != "" {
		deployArgs += " " + argStr
	}
	script := fmt.Sprintf("git clone %s /src && cd /src && git checkout %s && ap build %s && ap test %s && ap deploy %s",
		pkg.Spec.Repo, commit, argStr, argStr, deployArgs)

	jobName := JobName(pkg.Name, commit)
	jobNamespace := r.JobNamespace
	if jobNamespace == "" {
		jobNamespace = DefaultJobNamespace
	}

	labels := map[string]string{
		PackageNameLabel:      pkg.Name,
		PackageNamespaceLabel: pkg.Namespace,
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: jobNamespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: "autodeploy-controller",
					Containers: []corev1.Container{
						{
							Name:            "ap",
							Image:           image,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Command:         []string{"sh", "-c", script},
							Env: []corev1.EnvVar{
								{Name: "IMAGE_PREFIX", Value: r.ImagePrefix},
								{Name: "IMAGE_TAG", Value: r.ImageTag},
								{Name: "BUILDKIT_HOST", Value: r.BuildkitHost},
							},
						},
					},
				},
			},
		},
	}

	klog.Infof("Creating Job %s in namespace %s for package %s (commit %s)", jobName, jobNamespace, pkg.Name, commit)
	if err := r.Client.Create(ctx, job); err != nil {
		if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create Job: %w", err)
		}
		klog.Infof("Job %s already exists", jobName)
	}

	return nil
}
