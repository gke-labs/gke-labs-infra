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
	"context"
	"fmt"
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

// RunTask executes a single task, retrying if it implements Retryable according to its RetryPolicy.
func RunTask(ctx context.Context, scope *TaskContext, task Task) error {
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
