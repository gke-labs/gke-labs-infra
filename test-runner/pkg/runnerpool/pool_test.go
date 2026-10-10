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
	"errors"
	"strings"
	"testing"
	"time"

	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
)

func testOptions() Options {
	return Options{
		Name:              "test-runner",
		Replicas:          2,
		Labels:            []string{"test-runner"},
		RunnerGroupID:     1,
		Runner:            RunnerSpec{Image: "ghcr.io/actions/actions-runner:test", MaxLifetime: time.Hour},
		SyncPeriod:        time.Minute,
		OrphanSweepPeriod: 10 * time.Minute,
	}
}

func newTestPool(t *testing.T, opts Options, kube *fakeKube, gh *fakeGitHub) *Pool {
	t.Helper()
	p, err := New(opts, kube, gh)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }
	return p
}

func TestReconcileCreatesReplicas(t *testing.T) {
	ctx := t.Context()
	kube := newFakeKube()
	gh := newFakeGitHub()
	p := newTestPool(t, testOptions(), kube, gh)

	if err := p.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	names := kube.sandboxNames()
	if len(names) != 2 {
		t.Fatalf("expected 2 sandboxes, got %v", names)
	}
	for _, name := range names {
		if !strings.HasPrefix(name, "test-runner-") {
			t.Errorf("sandbox %q does not have the pool prefix", name)
		}
		sb := kube.sandboxes[name]
		if got := sb.Labels[PoolLabel]; got != "test-runner" {
			t.Errorf("sandbox %q pool label = %q", name, got)
		}
		if _, ok := runnerID(sb); !ok {
			t.Errorf("sandbox %q has no runner id annotation: %v", name, sb.Annotations)
		}
		secret, ok := kube.secrets[name]
		if !ok {
			t.Fatalf("no secret for sandbox %q", name)
		}
		if got := secret.StringData[jitConfigKey]; got != "jit-"+name {
			t.Errorf("secret %q jitconfig = %q", name, got)
		}
		if len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0].UID != sb.UID {
			t.Errorf("secret %q is not owned by its sandbox: %+v", name, secret.OwnerReferences)
		}
	}
	if len(gh.requests) != 2 {
		t.Fatalf("expected 2 JIT requests, got %d", len(gh.requests))
	}
	if got := gh.requests[0].Labels; len(got) != 1 || got[0] != "test-runner" {
		t.Errorf("JIT labels = %v", got)
	}

	// A second pass is a no-op.
	if err := p.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := kube.sandboxNames(); len(got) != 2 {
		t.Errorf("expected still 2 sandboxes, got %v", got)
	}
	if len(gh.requests) != 2 {
		t.Errorf("expected no new JIT requests, got %d", len(gh.requests))
	}
}

func TestReconcileReplacesFinishedSandboxes(t *testing.T) {
	ctx := t.Context()
	kube := newFakeKube()
	gh := newFakeGitHub()
	p := newTestPool(t, testOptions(), kube, gh)

	if err := p.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	before := kube.sandboxNames()
	finished := before[0]
	finishedID, _ := runnerID(kube.sandboxes[finished])
	kube.setFinished(finished, sandboxv1beta1.SandboxReasonPodSucceeded)

	if err := p.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	after := kube.sandboxNames()
	if len(after) != 2 {
		t.Fatalf("expected 2 sandboxes after replacement, got %v", after)
	}
	for _, name := range after {
		if name == finished {
			t.Errorf("finished sandbox %q was not deleted", finished)
		}
	}
	if _, ok := kube.secrets[finished]; ok {
		t.Errorf("secret for finished sandbox %q was not deleted", finished)
	}
	removed := false
	for _, id := range gh.removed {
		if id == finishedID {
			removed = true
		}
	}
	if !removed {
		t.Errorf("GitHub runner %d for finished sandbox was not removed (removed: %v)", finishedID, gh.removed)
	}
	if got := gh.runnerNames(); len(got) != 2 {
		t.Errorf("expected 2 GitHub runners, got %v", got)
	}
}

func TestReconcileRollsBackOnSandboxCreateFailure(t *testing.T) {
	ctx := t.Context()
	kube := newFakeKube()
	kube.failCreateSandbox = errors.New("boom")
	gh := newFakeGitHub()
	p := newTestPool(t, testOptions(), kube, gh)

	err := p.Reconcile(ctx)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected create failure, got %v", err)
	}
	if got := kube.sandboxNames(); len(got) != 0 {
		t.Errorf("expected no sandboxes, got %v", got)
	}
	if len(kube.secrets) != 0 {
		t.Errorf("expected secret to be rolled back, got %v", kube.secrets)
	}
	if got := gh.runnerNames(); len(got) != 0 {
		t.Errorf("expected GitHub runner to be rolled back, got %v", got)
	}
	// Only one attempt per pass, even though two replicas are missing.
	if len(gh.requests) != 1 {
		t.Errorf("expected 1 JIT request, got %d", len(gh.requests))
	}
}

func TestReconcileDoesNotCreateWhenGitHubFails(t *testing.T) {
	ctx := t.Context()
	kube := newFakeKube()
	gh := newFakeGitHub()
	gh.failJIT = errors.New("401 bad credentials")
	p := newTestPool(t, testOptions(), kube, gh)

	if err := p.Reconcile(ctx); err == nil {
		t.Fatal("expected error")
	}
	if got := kube.sandboxNames(); len(got) != 0 {
		t.Errorf("expected no sandboxes, got %v", got)
	}
	if len(kube.secrets) != 0 {
		t.Errorf("expected no secrets, got %v", kube.secrets)
	}
}

func TestSweepOrphans(t *testing.T) {
	ctx := t.Context()
	kube := newFakeKube()
	gh := newFakeGitHub()
	orphan := gh.addRunner("test-runner-orphan01", false)
	busy := gh.addRunner("test-runner-busy0001", true)
	other := gh.addRunner("other-pool-abcdefgh", false)

	p := newTestPool(t, testOptions(), kube, gh)
	if err := p.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	names := gh.runnerNames()
	for _, name := range names {
		if name == "test-runner-orphan01" {
			t.Errorf("orphaned runner %d was not removed: %v", orphan, names)
		}
	}
	if _, ok := gh.runners[busy]; !ok {
		t.Errorf("busy runner must not be removed")
	}
	if _, ok := gh.runners[other]; !ok {
		t.Errorf("runner from another pool must not be removed")
	}
	// The runners registered in this pass must survive the sweep.
	if got := len(gh.runnerNames()); got != 4 {
		t.Errorf("expected 4 runners (2 new + busy + other), got %v", gh.runnerNames())
	}
}

func TestNewValidation(t *testing.T) {
	base := testOptions()
	tests := []struct {
		name   string
		mutate func(*Options)
	}{
		{"empty name", func(o *Options) { o.Name = "" }},
		{"uppercase name", func(o *Options) { o.Name = "Runner" }},
		{"negative replicas", func(o *Options) { o.Replicas = -1 }},
		{"no labels", func(o *Options) { o.Labels = nil }},
		{"no image", func(o *Options) { o.Runner.Image = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := base
			tt.mutate(&o)
			if _, err := New(o, newFakeKube(), newFakeGitHub()); err == nil {
				t.Errorf("expected error")
			}
		})
	}
}
