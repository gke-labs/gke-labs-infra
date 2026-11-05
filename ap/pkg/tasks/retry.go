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

package tasks

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

// RetryPolicy reports whether a failed attempt (0-based) should be retried, and after how long.
type RetryPolicy interface {
	ShouldRetry(attempt int, err error) (wait time.Duration, retry bool)
}

// Retryable is an interface for tasks that declare a retry policy.
type Retryable interface {
	Task
	RetryPolicy() RetryPolicy
}

// Backoff is the simple policy: retry len(b) times with these waits.
type Backoff []time.Duration

func (b Backoff) ShouldRetry(attempt int, _ error) (time.Duration, bool) {
	if attempt < len(b) {
		return b[attempt], true
	}
	return 0, false
}

// RetryableTask wraps a Task with a RetryPolicy.
type RetryableTask struct {
	Task
	Policy RetryPolicy
}

func (r *RetryableTask) RetryPolicy() RetryPolicy {
	return r.Policy
}

func (r *RetryableTask) SourcePath() string {
	if s, ok := r.Task.(Sourced); ok {
		return s.SourcePath()
	}
	return ""
}

func (r *RetryableTask) WithChildren(children []Task) Task {
	if p, ok := r.Task.(Parent); ok {
		return &RetryableTask{
			Task:   p.WithChildren(children),
			Policy: r.Policy,
		}
	}
	return nil
}

// RunTask executes a single task, retrying if it implements Retryable according to its RetryPolicy.
// Leaf tasks (no children) run with buffered output printed on failure or at -v=1.
// Group tasks aggregate their children and do not buffer directly.
func RunTask(ctx context.Context, scope *TaskContext, task Task) error {
	if len(task.GetChildren()) > 0 {
		return runTaskWithRetry(ctx, scope, task)
	}
	return runLeafTask(ctx, scope, task)
}

var runnerMu sync.Mutex

func runLeafTask(ctx context.Context, scope *TaskContext, task Task) error {
	buf := &syncBuffer{}
	taskScope := scope.WithOutput(buf, buf)
	out := scope.Stdout()

	start := time.Now()
	err := runTaskWithRetry(ctx, taskScope, task)
	duration := time.Since(start)

	status := "ok"
	if err != nil {
		status = "FAILED"
	}

	runnerMu.Lock()
	defer runnerMu.Unlock()

	writef(out, "=== %s (%s, %.1fs)\n", task.GetName(), status, duration.Seconds())
	if err != nil || klog.V(1).Enabled() {
		output := buf.Bytes()
		if len(output) > 0 {
			if _, writeErr := out.Write(output); writeErr != nil {
				klog.V(2).Infof("failed to write task output: %v", writeErr)
			}
			if !bytes.HasSuffix(output, []byte("\n")) {
				writef(out, "\n")
			}
		}
	}
	return err
}

func runTaskWithRetry(ctx context.Context, scope *TaskContext, task Task) error {
	var policy RetryPolicy
	if r, ok := task.(Retryable); ok {
		policy = r.RetryPolicy()
	}

	for attempt := 0; ; attempt++ {
		err := task.Run(ctx, scope)
		if err == nil {
			return nil
		}
		if policy == nil {
			return err
		}
		wait, ok := policy.ShouldRetry(attempt, err)
		if !ok {
			return fmt.Errorf("task %s failed: %w", task.GetName(), err)
		}
		klog.Warningf("task %s failed (attempt %d): %v; retrying in %v...", task.GetName(), attempt+1, err, wait)
		select {
		case <-ctx.Done():
			return fmt.Errorf("task %s failed: %w", task.GetName(), ctx.Err())
		case <-time.After(wait):
		}
	}
}
