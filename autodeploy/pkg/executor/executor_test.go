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
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/autodeploy/pkg/apis/infra/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestJobName(t *testing.T) {
	tests := []struct {
		pkgName string
		commit  string
		want    string
	}{
		{
			pkgName: "app",
			commit:  "abc1234",
			want:    "deploy-app-abc1234",
		},
		{
			pkgName: "very-long-package-name-exceeding-standard-length-for-testing",
			commit:  "0123456789abcdef0123456789abcdef01234567",
			want:    "deploy-very-long-package-name-exceeding-standard-length-for-tes",
		},
	}

	for _, tc := range tests {
		got := JobName(tc.pkgName, tc.commit)
		if got != tc.want {
			t.Errorf("JobName(%q, %q) = %q, want %q", tc.pkgName, tc.commit, got, tc.want)
		}
		if len(got) > 63 {
			t.Errorf("JobName(%q, %q) length %d > 63", tc.pkgName, tc.commit, len(got))
		}
	}
}

func TestAPRunner_DeployFlow(t *testing.T) {
	ctx := t.Context()

	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add v1alpha1 scheme: %v", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add batchv1 scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add corev1 scheme: %v", err)
	}

	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	runner := &APRunner{
		Client:       client,
		ImagePrefix:  "test-registry.local",
		ImageTag:     "v1.0.0",
		BuildkitHost: "tcp://buildkit:2375",
	}

	pkg := &v1alpha1.Package{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sample-pkg",
			Namespace: "sample-ns",
		},
		Spec: v1alpha1.PackageSpec{
			Repo:      "https://example.com/repo.git",
			Directory: "sub/dir",
		},
	}

	commit := "abcdef1234567890"
	if err := runner.DeployFlow(ctx, pkg, commit, "sub/dir"); err != nil {
		t.Fatalf("DeployFlow failed: %v", err)
	}

	jobName := JobName(pkg.Name, commit)
	var job batchv1.Job
	if err := client.Get(ctx, types.NamespacedName{Namespace: DefaultJobNamespace, Name: jobName}, &job); err != nil {
		t.Fatalf("failed to get created job: %v", err)
	}

	// Verify labels
	if job.Labels[PackageNameLabel] != "sample-pkg" {
		t.Errorf("expected label %s=%s, got %s", PackageNameLabel, "sample-pkg", job.Labels[PackageNameLabel])
	}
	if job.Labels[PackageNamespaceLabel] != "sample-ns" {
		t.Errorf("expected label %s=%s, got %s", PackageNamespaceLabel, "sample-ns", job.Labels[PackageNamespaceLabel])
	}

	// Verify template labels
	if job.Spec.Template.Labels[PackageNameLabel] != "sample-pkg" {
		t.Errorf("expected template label %s=%s, got %s", PackageNameLabel, "sample-pkg", job.Spec.Template.Labels[PackageNameLabel])
	}

	// Verify container configuration
	if len(job.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(job.Spec.Template.Spec.Containers))
	}
	container := job.Spec.Template.Spec.Containers[0]
	if container.Image != "test-registry.local/ap-golang:latest" {
		t.Errorf("expected image test-registry.local/ap-golang:latest, got %s", container.Image)
	}
	if !strings.Contains(container.Command[2], "sub/dir") {
		t.Errorf("expected command script to contain args, got: %s", container.Command[2])
	}

	// Calling DeployFlow again for the same commit should be idempotent and not fail
	if err := runner.DeployFlow(ctx, pkg, commit, "sub/dir"); err != nil {
		t.Fatalf("idempotent DeployFlow failed: %v", err)
	}
}
