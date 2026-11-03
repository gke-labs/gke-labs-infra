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
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type mockRunner struct {
	runCount int
	args     []string
}

func (m *mockRunner) DeployFlow(ctx context.Context, pkg *v1alpha1.Package, commit string, args ...string) error {
	m.runCount++
	m.args = args
	return nil
}

func TestReconcile(t *testing.T) {
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

	// 2. Setup fake k8s client
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add scheme: %v", err)
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

	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.Package{}).WithRuntimeObjects(pkg).Build()

	// 3. Setup Reconciler
	runner := &mockRunner{}
	r := &PackageReconciler{
		Client: client,
		Scheme: scheme,
		Runner: runner,
	}

	// 4. Reconcile
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

	// 5. Verify
	if runner.runCount != 1 {
		t.Errorf("expected 1 ap run, got %d", runner.runCount)
	}
	if len(runner.args) != 1 || runner.args[0] != "testdir" {
		t.Errorf("expected [testdir], got %v", runner.args)
	}

	// Verify status update
	var updatedPkg v1alpha1.Package
	if err := client.Get(ctx, req.NamespacedName, &updatedPkg); err != nil {
		t.Fatalf("failed to get updated Package: %v", err)
	}

	if updatedPkg.Status.LastDeployedCommit != commitHash.String() {
		t.Errorf("expected LastDeployedCommit to be %s, got %s", commitHash.String(), updatedPkg.Status.LastDeployedCommit)
	}
}

func TestReconcile_InvalidInterval(t *testing.T) {
	ctx := t.Context()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add scheme: %v", err)
	}

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
