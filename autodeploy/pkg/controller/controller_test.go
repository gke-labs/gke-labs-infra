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
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gke-labs/gke-labs-infra/autodeploy/pkg/apis/infra/v1alpha1"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	batchv1 "k8s.io/api/batch/v1"
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

	jobName := fmt.Sprintf("deploy-%s-%s", pkg.Name, commit)
	if len(jobName) > 63 {
		jobName = jobName[:63]
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: "autodeploy-system",
		},
	}
	return m.client.Create(ctx, job)
}

func TestReconcile_SucceededJob(t *testing.T) {
	ctx := t.Context()

	// 1. Setup a local git repository
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

	// 2. Setup fake k8s client with extra schemas
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add scheme: %v", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add batch scheme: %v", err)
	}

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
		WithStatusSubresource(&v1alpha1.Package{}).
		WithStatusSubresource(&batchv1.Job{}).
		WithRuntimeObjects(pkg).
		Build()

	// 3. Setup Reconciler
	runner := &mockRunner{client: client}
	r := &PackageReconciler{
		Client: client,
		Scheme: scheme,
		Runner: runner,
	}

	// 4. Reconcile (First pass - triggers Job creation)
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-pkg",
			Namespace: "default",
		},
	}

	res, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Expect it to requeue in 5s because deployment is starting/running
	if res.RequeueAfter != 5*time.Second {
		t.Errorf("expected requeue in 5s, got %v", res.RequeueAfter)
	}

	// Verify status condition is Deploying
	var deployingPkg v1alpha1.Package
	if err := client.Get(ctx, req.NamespacedName, &deployingPkg); err != nil {
		t.Fatalf("failed to get deploying Package: %v", err)
	}
	if len(deployingPkg.Status.Conditions) == 0 || deployingPkg.Status.Conditions[0].Reason != "Deploying" {
		t.Errorf("expected condition Reason to be Deploying, got: %v", deployingPkg.Status.Conditions)
	}

	// 5. Verify Job is created and simulate success
	jobName := fmt.Sprintf("deploy-test-pkg-%s", commitHash.String())
	if len(jobName) > 63 {
		jobName = jobName[:63]
	}
	var job batchv1.Job
	if err := client.Get(ctx, types.NamespacedName{Name: jobName, Namespace: "autodeploy-system"}, &job); err != nil {
		t.Fatalf("expected Job to exist, got error: %v", err)
	}

	// Simulate Job success
	job.Status.Succeeded = 1
	if err := client.Status().Update(ctx, &job); err != nil {
		t.Fatalf("failed to update job status: %v", err)
	}

	// 6. Reconcile (Second pass - detects Job succeeded)
	res, err = r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Second Reconcile failed: %v", err)
	}

	// Expect standard requeue now
	if res.RequeueAfter == 5*time.Second {
		t.Errorf("expected standard requeue, got %v", res.RequeueAfter)
	}

	// Verify status updates
	var updatedPkg v1alpha1.Package
	if err := client.Get(ctx, req.NamespacedName, &updatedPkg); err != nil {
		t.Fatalf("failed to get updated Package: %v", err)
	}

	if updatedPkg.Status.LastDeployedCommit != commitHash.String() {
		t.Errorf("expected LastDeployedCommit to be %s, got %s", commitHash.String(), updatedPkg.Status.LastDeployedCommit)
	}

	if len(updatedPkg.Status.Conditions) == 0 || updatedPkg.Status.Conditions[0].Reason != "Succeeded" {
		t.Errorf("expected condition Reason to be Succeeded, got: %v", updatedPkg.Status.Conditions)
	}
}

func TestReconcile_FailedJob(t *testing.T) {
	ctx := t.Context()

	// 1. Setup a local git repository
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

	// 2. Setup fake k8s client with extra schemas
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add scheme: %v", err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add batch scheme: %v", err)
	}

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
		WithStatusSubresource(&v1alpha1.Package{}).
		WithStatusSubresource(&batchv1.Job{}).
		WithRuntimeObjects(pkg).
		Build()

	// 3. Setup Reconciler
	runner := &mockRunner{client: client}
	r := &PackageReconciler{
		Client: client,
		Scheme: scheme,
		Runner: runner,
	}

	// 4. Reconcile (First pass - triggers Job creation)
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-pkg",
			Namespace: "default",
		},
	}

	_, err = r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// 5. Verify Job is created and simulate failure
	jobName := fmt.Sprintf("deploy-test-pkg-%s", commitHash.String())
	if len(jobName) > 63 {
		jobName = jobName[:63]
	}
	var job batchv1.Job
	if err := client.Get(ctx, types.NamespacedName{Name: jobName, Namespace: "autodeploy-system"}, &job); err != nil {
		t.Fatalf("expected Job to exist, got error: %v", err)
	}

	// Simulate Job failure
	job.Status.Failed = 1
	if err := client.Status().Update(ctx, &job); err != nil {
		t.Fatalf("failed to update job status: %v", err)
	}

	// 6. Reconcile (Second pass - detects Job failed)
	res, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Second Reconcile failed: %v", err)
	}

	// Expect standard requeue now
	if res.RequeueAfter == 5*time.Second {
		t.Errorf("expected standard requeue, got %v", res.RequeueAfter)
	}

	// Verify status updates (LastDeployedCommit must NOT be updated)
	var updatedPkg v1alpha1.Package
	if err := client.Get(ctx, req.NamespacedName, &updatedPkg); err != nil {
		t.Fatalf("failed to get updated Package: %v", err)
	}

	if updatedPkg.Status.LastDeployedCommit != "" {
		t.Errorf("expected LastDeployedCommit to be empty, got %s", updatedPkg.Status.LastDeployedCommit)
	}

	if len(updatedPkg.Status.Conditions) == 0 || updatedPkg.Status.Conditions[0].Reason != "Failed" {
		t.Errorf("expected condition Reason to be Failed, got: %v", updatedPkg.Status.Conditions)
	}
}
