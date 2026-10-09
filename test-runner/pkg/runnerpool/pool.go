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

// Package runnerpool keeps a fixed number of ephemeral GitHub Actions runners
// available, each running inside an agent-sandbox Sandbox.
//
// Every runner is registered with GitHub using a just-in-time (JIT)
// configuration, which makes it single-use: once it has run one job the
// runner process exits, the Sandbox reports Finished, and the pool deletes
// it and registers a fresh one. The JIT configuration is the only credential
// that reaches the sandbox, and it is only good for that one runner.
package runnerpool

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/klog/v2"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"

	"github.com/gke-labs/gke-labs-infra/test-runner/pkg/github"
)

// Options configures a Pool.
type Options struct {
	// Name identifies the pool; it prefixes sandbox and runner names and is
	// stored in the PoolLabel.
	Name string
	// Replicas is the number of runners to keep registered and waiting for
	// jobs (plus any that are currently busy, which are replaced as they
	// finish).
	Replicas int
	// Labels are the GitHub runner labels. Workflows select the pool with
	// "runs-on: [self-hosted, <label>]".
	Labels []string
	// RunnerGroupID is the GitHub runner group to register into (1 is the
	// default group).
	RunnerGroupID int64
	// Runner describes the sandbox pods.
	Runner RunnerSpec
	// SyncPeriod is how often the pool reconciles even if nothing changed.
	SyncPeriod time.Duration
	// OrphanSweepPeriod is how often runners that exist in GitHub without a
	// sandbox are cleaned up. Zero disables the sweep.
	OrphanSweepPeriod time.Duration
}

// Pool reconciles a set of runner sandboxes against the desired replica count.
type Pool struct {
	opts   Options
	kube   KubeClient
	github github.Runners
	now    func() time.Time

	mu sync.Mutex
	// recentlyCreated tracks runner registrations that may not yet be
	// visible as sandboxes, so the orphan sweep leaves them alone.
	recentlyCreated map[int64]time.Time
	lastOrphanSweep time.Time
}

// New returns a Pool. It does not start reconciling until Run is called.
func New(opts Options, kube KubeClient, gh github.Runners) (*Pool, error) {
	if opts.Name == "" {
		return nil, fmt.Errorf("pool name is required")
	}
	if errs := validatePoolName(opts.Name); len(errs) > 0 {
		return nil, fmt.Errorf("invalid pool name %q: %s", opts.Name, strings.Join(errs, "; "))
	}
	if opts.Replicas < 0 {
		return nil, fmt.Errorf("replicas must not be negative")
	}
	if len(opts.Labels) == 0 {
		return nil, fmt.Errorf("at least one runner label is required")
	}
	if opts.Runner.Image == "" {
		return nil, fmt.Errorf("runner image is required")
	}
	if opts.SyncPeriod <= 0 {
		opts.SyncPeriod = 30 * time.Second
	}
	return &Pool{
		opts:            opts,
		kube:            kube,
		github:          gh,
		now:             time.Now,
		recentlyCreated: map[int64]time.Time{},
	}, nil
}

// Run reconciles until ctx is cancelled. A reconcile happens every
// SyncPeriod and whenever a value is received on trigger (which may be nil).
func (p *Pool) Run(ctx context.Context, trigger <-chan struct{}) error {
	ticker := time.NewTicker(p.opts.SyncPeriod)
	defer ticker.Stop()

	for {
		if err := p.Reconcile(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			klog.Errorf("reconcile failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-trigger:
		}
	}
}

// Reconcile performs a single pass: it removes finished sandboxes, creates
// missing runners and (periodically) removes orphaned GitHub runners.
func (p *Pool) Reconcile(ctx context.Context) error {
	selector := labels.SelectorFromSet(labels.Set{PoolLabel: p.opts.Name}).String()
	sandboxes, err := p.kube.ListSandboxes(ctx, selector)
	if err != nil {
		return err
	}

	var errs []error
	active := 0
	for i := range sandboxes {
		sb := &sandboxes[i]
		if sb.DeletionTimestamp != nil {
			continue
		}
		if isFinished(sb) {
			klog.Infof("sandbox %s finished, removing", sb.Name)
			if err := p.removeSandbox(ctx, sb); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		active++
	}

	for i := active; i < p.opts.Replicas; i++ {
		if err := p.createRunner(ctx); err != nil {
			errs = append(errs, err)
			// Creating runners tends to fail for a shared reason (GitHub
			// auth, quota); do not hammer it in a single pass.
			break
		}
	}

	if p.orphanSweepDue() {
		if err := p.sweepOrphans(ctx, sandboxes); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// removeSandbox deletes a sandbox and deregisters its GitHub runner. The
// Secret is garbage collected through its owner reference.
func (p *Pool) removeSandbox(ctx context.Context, sb *sandboxv1beta1.Sandbox) error {
	if id, ok := runnerID(sb); ok {
		if err := p.github.RemoveRunner(ctx, id); err != nil {
			return fmt.Errorf("removing GitHub runner for sandbox %s: %w", sb.Name, err)
		}
	}
	if err := p.kube.DeleteSandbox(ctx, sb.Name); err != nil {
		return err
	}
	// Belt and braces: the owner reference should have handled this, but
	// make sure a Secret never outlives its sandbox.
	return p.kube.DeleteSecret(ctx, sb.Name)
}

// createRunner registers a JIT runner with GitHub and creates the Secret and
// Sandbox that run it. On failure everything created so far is rolled back.
func (p *Pool) createRunner(ctx context.Context) (retErr error) {
	name := p.opts.Name + "-" + rand.String(8)

	jit, err := p.github.GenerateJITConfig(ctx, github.JITRequest{
		Name:          name,
		Labels:        p.opts.Labels,
		RunnerGroupID: p.opts.RunnerGroupID,
	})
	if err != nil {
		return err
	}
	p.markCreated(jit.ID)
	defer func() {
		if retErr != nil {
			if err := p.github.RemoveRunner(ctx, jit.ID); err != nil {
				klog.Warningf("rolling back GitHub runner %s (%d): %v", name, jit.ID, err)
			}
		}
	}()

	if _, err := p.kube.CreateSecret(ctx, buildSecret(p.opts.Name, name, jit.EncodedConfig)); err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			if err := p.kube.DeleteSecret(ctx, name); err != nil {
				klog.Warningf("rolling back secret %s: %v", name, err)
			}
		}
	}()

	sb := buildSandbox(p.opts.Name, name, p.opts.Runner, p.now())
	sb.Annotations = map[string]string{RunnerIDAnnotation: strconv.FormatInt(jit.ID, 10)}
	created, err := p.kube.CreateSandbox(ctx, sb)
	if err != nil {
		return err
	}
	if err := p.kube.SetSecretOwner(ctx, name, sandboxOwnerRef(created)); err != nil {
		if derr := p.kube.DeleteSandbox(ctx, name); derr != nil {
			klog.Warningf("rolling back sandbox %s: %v", name, derr)
		}
		return err
	}
	klog.Infof("created runner %s (GitHub runner id %d)", name, jit.ID)
	return nil
}

// sweepOrphans removes GitHub runners that belong to this pool but have no
// sandbox, e.g. because the controller crashed between registering a runner
// and creating its sandbox, or because the sandbox was deleted by hand.
func (p *Pool) sweepOrphans(ctx context.Context, sandboxes []sandboxv1beta1.Sandbox) error {
	runners, err := p.github.ListRunners(ctx)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for i := range sandboxes {
		known[sandboxes[i].Name] = true
	}
	prefix := p.opts.Name + "-"
	var errs []error
	for _, r := range runners {
		if !strings.HasPrefix(r.Name, prefix) || known[r.Name] || r.Busy || p.isRecentlyCreated(r.ID) {
			continue
		}
		klog.Infof("removing orphaned GitHub runner %s (%d, status %s)", r.Name, r.ID, r.Status)
		if err := p.github.RemoveRunner(ctx, r.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (p *Pool) orphanSweepDue() bool {
	if p.opts.OrphanSweepPeriod <= 0 {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if now.Sub(p.lastOrphanSweep) < p.opts.OrphanSweepPeriod {
		return false
	}
	p.lastOrphanSweep = now
	return true
}

// recentlyCreatedGrace is how long a freshly registered runner is protected
// from the orphan sweep.
const recentlyCreatedGrace = 10 * time.Minute

func (p *Pool) markCreated(id int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for k, t := range p.recentlyCreated {
		if now.Sub(t) > recentlyCreatedGrace {
			delete(p.recentlyCreated, k)
		}
	}
	p.recentlyCreated[id] = now
}

func (p *Pool) isRecentlyCreated(id int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, ok := p.recentlyCreated[id]
	return ok && p.now().Sub(t) <= recentlyCreatedGrace
}

func runnerID(sb *sandboxv1beta1.Sandbox) (int64, bool) {
	v, ok := sb.Annotations[RunnerIDAnnotation]
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id == 0 {
		return 0, false
	}
	return id, true
}

// validatePoolName checks that the pool name can be used as a prefix for
// sandbox, pod and secret names.
func validatePoolName(name string) []string {
	var errs []string
	if len(name) > 40 {
		errs = append(errs, "must be at most 40 characters")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			errs = append(errs, "must consist of lowercase letters, digits and '-'")
			break
		}
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		errs = append(errs, "must not start or end with '-'")
	}
	return errs
}
