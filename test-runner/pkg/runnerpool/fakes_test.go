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
	"context"
	"fmt"
	"sort"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"

	"github.com/gke-labs/gke-labs-infra/test-runner/pkg/github"
)

// fakeKube is an in-memory KubeClient.
type fakeKube struct {
	mu        sync.Mutex
	sandboxes map[string]*sandboxv1beta1.Sandbox
	secrets   map[string]*corev1.Secret
	nextUID   int

	// failCreateSandbox makes CreateSandbox fail when set.
	failCreateSandbox error
}

func newFakeKube() *fakeKube {
	return &fakeKube{
		sandboxes: map[string]*sandboxv1beta1.Sandbox{},
		secrets:   map[string]*corev1.Secret{},
	}
}

func (f *fakeKube) ListSandboxes(_ context.Context, labelSelector string) ([]sandboxv1beta1.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sel, err := labels.Parse(labelSelector)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(f.sandboxes))
	for name := range f.sandboxes {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []sandboxv1beta1.Sandbox
	for _, name := range names {
		sb := f.sandboxes[name]
		if sel.Matches(labels.Set(sb.Labels)) {
			out = append(out, *sb.DeepCopy())
		}
	}
	return out, nil
}

func (f *fakeKube) CreateSandbox(_ context.Context, sb *sandboxv1beta1.Sandbox) (*sandboxv1beta1.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCreateSandbox != nil {
		return nil, f.failCreateSandbox
	}
	if _, exists := f.sandboxes[sb.Name]; exists {
		return nil, fmt.Errorf("sandbox %q already exists", sb.Name)
	}
	f.nextUID++
	created := sb.DeepCopy()
	created.UID = types.UID(fmt.Sprintf("uid-%d", f.nextUID))
	f.sandboxes[sb.Name] = created
	return created.DeepCopy(), nil
}

func (f *fakeKube) DeleteSandbox(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sandboxes, name)
	// Emulate garbage collection of owned secrets.
	for sname, s := range f.secrets {
		for _, o := range s.OwnerReferences {
			if o.Kind == "Sandbox" && o.Name == name {
				delete(f.secrets, sname)
			}
		}
	}
	return nil
}

func (f *fakeKube) CreateSecret(_ context.Context, s *corev1.Secret) (*corev1.Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.secrets[s.Name]; exists {
		return nil, fmt.Errorf("secret %q already exists", s.Name)
	}
	f.secrets[s.Name] = s.DeepCopy()
	return s.DeepCopy(), nil
}

func (f *fakeKube) SetSecretOwner(_ context.Context, name string, owner metav1.OwnerReference) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.secrets[name]
	if !ok {
		return fmt.Errorf("secret %q not found", name)
	}
	s.OwnerReferences = []metav1.OwnerReference{owner}
	return nil
}

func (f *fakeKube) DeleteSecret(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.secrets, name)
	return nil
}

// setFinished marks a sandbox as having a terminated pod.
func (f *fakeKube) setFinished(name string, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sb := f.sandboxes[name]
	sb.Status.Conditions = []metav1.Condition{
		{Type: string(sandboxv1beta1.SandboxConditionReady), Status: metav1.ConditionFalse, Reason: reason},
		{Type: string(sandboxv1beta1.SandboxConditionFinished), Status: metav1.ConditionTrue, Reason: reason},
	}
}

func (f *fakeKube) sandboxNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for name := range f.sandboxes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// fakeGitHub is an in-memory github.Runners.
type fakeGitHub struct {
	mu      sync.Mutex
	nextID  int64
	runners map[int64]github.Runner
	// requests records every JIT request made.
	requests []github.JITRequest
	// failJIT makes GenerateJITConfig fail when set.
	failJIT error
	removed []int64
}

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{runners: map[int64]github.Runner{}}
}

func (f *fakeGitHub) GenerateJITConfig(_ context.Context, req github.JITRequest) (*github.JITRunner, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failJIT != nil {
		return nil, f.failJIT
	}
	f.requests = append(f.requests, req)
	f.nextID++
	f.runners[f.nextID] = github.Runner{ID: f.nextID, Name: req.Name, Status: "offline"}
	return &github.JITRunner{ID: f.nextID, EncodedConfig: "jit-" + req.Name}, nil
}

func (f *fakeGitHub) RemoveRunner(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.runners, id)
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeGitHub) ListRunners(_ context.Context) ([]github.Runner, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []github.Runner
	for _, r := range f.runners {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// addRunner registers a runner directly, as if it had been created by a
// previous controller instance.
func (f *fakeGitHub) addRunner(name string, busy bool) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.runners[f.nextID] = github.Runner{ID: f.nextID, Name: name, Status: "offline", Busy: busy}
	return f.nextID
}

func (f *fakeGitHub) runnerNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for _, r := range f.runners {
		names = append(names, r.Name)
	}
	sort.Strings(names)
	return names
}
