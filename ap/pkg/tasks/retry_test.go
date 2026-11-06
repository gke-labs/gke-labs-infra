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
	"errors"
	"flag"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"k8s.io/klog/v2"
)

func newTestScope(t *testing.T) *TaskContext {
	dir := t.TempDir()
	tree, err := sourcetree.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return NewTaskContext(dir, dir, tree)
}

type fakeRetryableTask struct {
	name     string
	attempts int
	failTill int
	policy   RetryPolicy
	err      error
}

func (t *fakeRetryableTask) Run(ctx context.Context, scope *TaskContext) error {
	t.attempts++
	if t.attempts <= t.failTill {
		if t.err != nil {
			return t.err
		}
		return errors.New("transient error")
	}
	return nil
}

func (t *fakeRetryableTask) GetName() string {
	return t.name
}

func (t *fakeRetryableTask) GetChildren() []Task {
	return nil
}

func (t *fakeRetryableTask) RetryPolicy() RetryPolicy {
	return t.policy
}

// selectiveRetryPolicy retries errors unless they match nonRetryErr.
type selectiveRetryPolicy struct {
	backoff     time.Duration
	nonRetryErr error
}

func (p *selectiveRetryPolicy) ShouldRetry(_ int, err error) (time.Duration, bool) {
	if errors.Is(err, p.nonRetryErr) {
		return 0, false
	}
	return p.backoff, true
}

func TestRunTask_Retry(t *testing.T) {
	t.Run("fails twice then succeeds", func(t *testing.T) {
		fs := flag.NewFlagSet("klog", flag.ContinueOnError)
		klog.InitFlags(fs)
		_ = fs.Set("v", "1")
		defer fs.Set("v", "0")

		task := &fakeRetryableTask{
			name:     "fake-task",
			failTill: 2,
			policy: Backoff{
				10 * time.Millisecond,
				20 * time.Millisecond,
			},
		}

		var outBuf syncBuffer
		scope := newTestScope(t).WithOutput(&outBuf, &outBuf)
		start := time.Now()
		err := RunTask(t.Context(), scope, task)
		elapsed := time.Since(start)

		if err != nil {
			t.Fatalf("expected RunTask to succeed, got: %v", err)
		}
		if task.attempts != 3 {
			t.Errorf("expected 3 attempts (2 failures + 1 success), got %d", task.attempts)
		}
		if elapsed < 30*time.Millisecond {
			t.Errorf("expected elapsed >= 30ms, got %v", elapsed)
		}

		logOutput := outBuf.String()
		if !strings.Contains(logOutput, `"task"="fake-task"`) || !strings.Contains(logOutput, `"attempt"=1`) {
			t.Errorf("expected log to contain retry warning for attempt 1, got: %s", logOutput)
		}
		if !strings.Contains(logOutput, `"wait"="10ms"`) {
			t.Errorf("expected log to contain '\"wait\"=\"10ms\"', got: %s", logOutput)
		}
		if !strings.Contains(logOutput, `"attempt"=2`) {
			t.Errorf("expected log to contain retry warning for attempt 2, got: %s", logOutput)
		}
		if !strings.Contains(logOutput, `"wait"="20ms"`) {
			t.Errorf("expected log to contain '\"wait\"=\"20ms\"', got: %s", logOutput)
		}
	})

	t.Run("exhausts retries and returns wrapped last error", func(t *testing.T) {
		sentinelErr := errors.New("persistent failure")
		task := &fakeRetryableTask{
			name:     "fake-task",
			failTill: 10,
			err:      sentinelErr,
			policy: Backoff{
				time.Millisecond,
				time.Millisecond,
			},
		}

		var outBuf syncBuffer
		scope := newTestScope(t).WithOutput(&outBuf, &outBuf)
		err := RunTask(t.Context(), scope, task)
		if err == nil {
			t.Fatal("expected RunTask to fail on persistent error, got nil")
		}
		if task.attempts != 3 {
			t.Errorf("expected 3 attempts, got %d", task.attempts)
		}
		if !errors.Is(err, sentinelErr) {
			t.Errorf("expected error to wrap sentinel error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "task fake-task failed") {
			t.Errorf("expected error message to contain task name, got: %v", err)
		}
		if !strings.Contains(err.Error(), "persistent failure") {
			t.Errorf("expected error message to contain persistent failure, got: %v", err)
		}

		logOutput := outBuf.String()
		if !strings.Contains(logOutput, `"task"="fake-task"`) || !strings.Contains(logOutput, `"attempt"=1`) {
			t.Errorf("expected log to contain retry warning for attempt 1, got: %s", logOutput)
		}
		if !strings.Contains(logOutput, `"attempt"=2`) {
			t.Errorf("expected log to contain retry warning for attempt 2, got: %s", logOutput)
		}
	})

	t.Run("policy refuses to retry specific error", func(t *testing.T) {
		fatalErr := errors.New("non-retryable fatal error")
		task := &fakeRetryableTask{
			name:     "fake-task",
			failTill: 10,
			err:      fatalErr,
			policy: &selectiveRetryPolicy{
				backoff:     10 * time.Millisecond,
				nonRetryErr: fatalErr,
			},
		}

		var outBuf syncBuffer
		scope := newTestScope(t).WithOutput(&outBuf, &outBuf)
		err := RunTask(t.Context(), scope, task)
		if err == nil {
			t.Fatal("expected RunTask to fail immediately on refused error, got nil")
		}
		if task.attempts != 1 {
			t.Errorf("expected exactly 1 attempt when policy refuses error, got %d", task.attempts)
		}
		if !errors.Is(err, fatalErr) {
			t.Errorf("expected error to wrap fatalErr, got: %v", err)
		}
		if !strings.Contains(err.Error(), "task fake-task failed") {
			t.Errorf("expected error message to contain task name, got: %v", err)
		}

		logOutput := outBuf.String()
		if strings.Contains(logOutput, "retrying") {
			t.Errorf("expected no retry warnings in log, got: %s", logOutput)
		}
	})

	t.Run("context cancellation aborts retry", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		task := &fakeRetryableTask{
			name:     "fake-task",
			failTill: 10,
			policy: Backoff{
				500 * time.Millisecond,
				500 * time.Millisecond,
			},
		}

		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()

		var outBuf syncBuffer
		scope := newTestScope(t).WithOutput(&outBuf, &outBuf)
		err := RunTask(ctx, scope, task)
		if err == nil {
			t.Fatal("expected error on cancelled context, got nil")
		}
		if !strings.Contains(err.Error(), "task fake-task failed") {
			t.Errorf("expected error to contain task name, got: %v", err)
		}
		if !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("expected error to contain 'context canceled', got: %v", err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected errors.Is(err, context.Canceled) to be true, got: %v", err)
		}
	})

	t.Run("non-retryable task runs once and is not retried", func(t *testing.T) {
		sentinelErr := errors.New("non-retryable failure")
		var ranCount int
		task := &FuncTask{
			Name: "plain-task",
			RunFn: func(ctx context.Context, scope *TaskContext) error {
				ranCount++
				return sentinelErr
			},
		}

		scope := newTestScope(t)
		err := RunTask(t.Context(), scope, task)
		if ranCount != 1 {
			t.Errorf("expected 1 attempt, got %d", ranCount)
		}
		if !errors.Is(err, sentinelErr) {
			t.Errorf("expected error to be sentinelErr directly, got: %v", err)
		}
	})
}
