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

package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gke-labs/gke-labs-infra/autodeploy/pkg/apis/infra/v1alpha1"
	"github.com/gke-labs/gke-labs-infra/autodeploy/pkg/executor"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type mockRunner struct {
	client   client.Client
	runCount int
	args     []string
}

func (m *mockRunner) DeployFlow(ctx context.Context, pkg *v1alpha1.Package, commit string, args ...string) error {
	m.runCount++
	m.args = args
	if m.client != nil {
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      executor.JobName(pkg.Name, commit),
				Namespace: executor.DefaultJobNamespace,
				Labels: map[string]string{
					executor.PackageNameLabel:      pkg.Name,
					executor.PackageNamespaceLabel: pkg.Namespace,
				},
			},
		}
		return m.client.Create(ctx, job)
	}
	return nil
}

func setupTestRepo(t *testing.T) (string, string) {
	t.Helper()
	repoPath := t.TempDir()
	repo, err := git.PlainInit(repoPath, false)
	if err != nil {
		t.Fatalf("failed to init repo: %v", err)
	}
	w, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	testFile := filepath.Join(repoPath, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add file: %v", err)
	}
	commitHash, err := w.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}
	return repoPath, commitHash.String()
}

func setupScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
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
	return scheme
}

func TestReconcile(t *testing.T) {
	ctx := t.Context()
	repoPath, commit := setupTestRepo(t)
	scheme := setupScheme(t)

	pkg := &v1alpha1.Package{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pkg",
			Namespace: "default",
		},
		Spec: v1alpha1.PackageSpec{
			Repo:      repoPath,
			Branch:    "master",
			Directory: "testdir",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.Package{}, &batchv1.Job{}).
		WithRuntimeObjects(pkg).
		Build()

	runner := &mockRunner{client: client}
	r := &PackageReconciler{
		Client: client,
		Scheme: scheme,
		Runner: runner,
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-pkg",
			Namespace: "default",
		},
	}

	// 1. First Reconcile: Job does not exist yet.
	// It should trigger DeployFlow and requeue, but NOT update LastDeployedCommit yet.
	res, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("First Reconcile failed: %v", err)
	}
	if res.RequeueAfter != 10*time.Second {
		t.Errorf("expected RequeueAfter 10s, got %v", res.RequeueAfter)
	}
	if runner.runCount != 1 {
		t.Errorf("expected 1 ap run, got %d", runner.runCount)
	}
	if len(runner.args) != 1 || runner.args[0] != "testdir" {
		t.Errorf("expected [testdir], got %v", runner.args)
	}

	var updatedPkg v1alpha1.Package
	if err := client.Get(ctx, req.NamespacedName, &updatedPkg); err != nil {
		t.Fatalf("failed to get Package: %v", err)
	}
	if updatedPkg.Status.LastDeployedCommit != "" {
		t.Errorf("expected LastDeployedCommit to be empty while Job is pending, got %s", updatedPkg.Status.LastDeployedCommit)
	}

	// 2. Second Reconcile: Job exists but is still running.
	// Should not re-trigger DeployFlow and should not update status.
	res, err = r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Second Reconcile failed: %v", err)
	}
	if res.RequeueAfter != 10*time.Second {
		t.Errorf("expected RequeueAfter 10s, got %v", res.RequeueAfter)
	}
	if runner.runCount != 1 {
		t.Errorf("expected runner.runCount to remain 1, got %d", runner.runCount)
	}

	// 3. Mark the Job as completed successfully.
	jobName := executor.JobName(pkg.Name, commit)
	var job batchv1.Job
	if err := client.Get(ctx, types.NamespacedName{Namespace: executor.DefaultJobNamespace, Name: jobName}, &job); err != nil {
		t.Fatalf("failed to get created job: %v", err)
	}
	job.Status.Conditions = []batchv1.JobCondition{
		{
			Type:   batchv1.JobComplete,
			Status: corev1.ConditionTrue,
		},
	}
	if err := client.Status().Update(ctx, &job); err != nil {
		t.Fatalf("failed to update job status: %v", err)
	}

	// 4. Third Reconcile: Job has succeeded.
	// Should update LastDeployedCommit to commit and requeue with pollInterval.
	res, err = r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Third Reconcile failed: %v", err)
	}
	if res.RequeueAfter != 1*time.Minute {
		t.Errorf("expected RequeueAfter 1m, got %v", res.RequeueAfter)
	}

	if err := client.Get(ctx, req.NamespacedName, &updatedPkg); err != nil {
		t.Fatalf("failed to get updated Package: %v", err)
	}
	if updatedPkg.Status.LastDeployedCommit != commit {
		t.Errorf("expected LastDeployedCommit to be %s, got %s", commit, updatedPkg.Status.LastDeployedCommit)
	}

	// 5. Fourth Reconcile: Already deployed commit, no op.
	res, err = r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Fourth Reconcile failed: %v", err)
	}
	if res.RequeueAfter != 1*time.Minute {
		t.Errorf("expected RequeueAfter 1m, got %v", res.RequeueAfter)
	}
	if runner.runCount != 1 {
		t.Errorf("expected runner.runCount to remain 1, got %d", runner.runCount)
	}
}

func TestReconcile_JobFailed(t *testing.T) {
	ctx := t.Context()
	repoPath, commit := setupTestRepo(t)
	scheme := setupScheme(t)

	pkg := &v1alpha1.Package{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pkg-fail",
			Namespace: "default",
		},
		Spec: v1alpha1.PackageSpec{
			Repo:   repoPath,
			Branch: "master",
		},
	}

	jobName := executor.JobName(pkg.Name, commit)
	failedJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: executor.DefaultJobNamespace,
			Labels: map[string]string{
				executor.PackageNameLabel:      pkg.Name,
				executor.PackageNamespaceLabel: pkg.Namespace,
			},
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{
					Type:    batchv1.JobFailed,
					Status:  corev1.ConditionTrue,
					Reason:  "BackoffLimitExceeded",
					Message: "Job has reached the specified backoff limit",
				},
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.Package{}, &batchv1.Job{}).
		WithRuntimeObjects(pkg, failedJob).
		Build()

	runner := &mockRunner{client: client}
	r := &PackageReconciler{
		Client: client,
		Scheme: scheme,
		Runner: runner,
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-pkg-fail",
			Namespace: "default",
		},
	}

	res, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if res.RequeueAfter != 1*time.Minute {
		t.Errorf("expected RequeueAfter 1m, got %v", res.RequeueAfter)
	}
	if runner.runCount != 0 {
		t.Errorf("expected DeployFlow not to be called for existing failed job, got %d", runner.runCount)
	}

	var updatedPkg v1alpha1.Package
	if err := client.Get(ctx, req.NamespacedName, &updatedPkg); err != nil {
		t.Fatalf("failed to get Package: %v", err)
	}
	if updatedPkg.Status.LastDeployedCommit != "" {
		t.Errorf("expected LastDeployedCommit to remain empty on failure, got %s", updatedPkg.Status.LastDeployedCommit)
	}
}

func TestFindPackagesForJob(t *testing.T) {
	ctx := t.Context()
	r := &PackageReconciler{}

	// Case 1: Job with valid package labels
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "deploy-my-pkg-12345",
			Namespace: "autodeploy-system",
			Labels: map[string]string{
				executor.PackageNameLabel:      "my-pkg",
				executor.PackageNamespaceLabel: "custom-ns",
			},
		},
	}
	requests := r.findPackagesForJob(ctx, job)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].Name != "my-pkg" || requests[0].Namespace != "custom-ns" {
		t.Errorf("unexpected request: %v", requests[0])
	}

	// Case 2: Job without labels
	jobNoLabels := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unrelated-job",
			Namespace: "autodeploy-system",
		},
	}
	if reqs := r.findPackagesForJob(ctx, jobNoLabels); len(reqs) != 0 {
		t.Errorf("expected 0 requests for job without labels, got %v", reqs)
	}

	// Case 3: Other object type
	pod := &corev1.Pod{}
	if reqs := r.findPackagesForJob(ctx, pod); len(reqs) != 0 {
		t.Errorf("expected 0 requests for non-Job object, got %v", reqs)
	}
}

func TestReconcile_InvalidInterval(t *testing.T) {
	ctx := t.Context()
	scheme := setupScheme(t)

	pkg := &v1alpha1.Package{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pkg-invalid-interval",
			Namespace: "default",
		},
		Spec: v1alpha1.PackageSpec{
			Repo:     "http://example.com/repo.git",
			Interval: "not-a-valid-duration",
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(pkg).Build()
	r := &PackageReconciler{
		Client: client,
		Scheme: scheme,
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-pkg-invalid-interval",
			Namespace: "default",
		},
	}

	_, err := r.Reconcile(ctx, req)
	if err == nil {
		t.Fatalf("expected error for invalid interval, got nil")
	}
}
