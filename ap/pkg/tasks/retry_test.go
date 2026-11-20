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
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"k8s.io/klog/v2"
)

func newTestScope(t *testing.T) *TaskContext {
	dir := t.TempDir()
	tree, err := sourcetree.Open(t.Context(), dir)
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
		if !strings.Contains(logOutput, "task=fake-task") || !strings.Contains(logOutput, "attempt=1") {
			t.Errorf("expected log to contain retry warning for attempt 1, got: %s", logOutput)
		}
		if !strings.Contains(logOutput, "wait=10ms") {
			t.Errorf("expected log to contain 'wait=10ms', got: %s", logOutput)
		}
		if !strings.Contains(logOutput, "attempt=2") {
			t.Errorf("expected log to contain retry warning for attempt 2, got: %s", logOutput)
		}
		if !strings.Contains(logOutput, "wait=20ms") {
			t.Errorf("expected log to contain 'wait=20ms', got: %s", logOutput)
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
		if !strings.Contains(logOutput, "task=fake-task") || !strings.Contains(logOutput, "attempt=1") {
			t.Errorf("expected log to contain retry warning for attempt 1, got: %s", logOutput)
		}
		if !strings.Contains(logOutput, "attempt=2") {
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

func TestBufferLogger_Rendering(t *testing.T) {
	var buf bytes.Buffer
	var warnings syncBuffer
	logger := newBufferLogger(&buf, &warnings)

	logger.Info("starting port-forward", "namespace", "buildkit", "service", "buildkitd")
	logger.Info("copying file", "path", "/path with spaces/file")
	logger.Error("failed to connect", "error", errors.New("connection reset"), "service", "buildkitd")
	withValLogger := logger.With("component", "portforward")
	withValLogger.Info("ready", "port", 1234)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 log lines, got %d:\n%s", len(lines), buf.String())
	}

	want0 := "starting port-forward namespace=buildkit service=buildkitd"
	if lines[0] != want0 {
		t.Errorf("line 0 = %q, want %q", lines[0], want0)
	}

	want1 := `copying file path="/path with spaces/file"`
	if lines[1] != want1 {
		t.Errorf("line 1 = %q, want %q", lines[1], want1)
	}

	want2 := `failed to connect error="connection reset" service=buildkitd`
	if lines[2] != want2 {
		t.Errorf("line 2 = %q, want %q", lines[2], want2)
	}

	want3 := "ready component=portforward port=1234"
	if lines[3] != want3 {
		t.Errorf("line 3 = %q, want %q", lines[3], want3)
	}

	if warnings.String() != "" {
		t.Errorf("expected no warnings, got %q", warnings.String())
	}

	logger.Warn("something warned", "reason", "test")
	warnLines := strings.Split(strings.TrimSpace(warnings.String()), "\n")
	if len(warnLines) != 1 || warnLines[0] != "something warned level=warning reason=test" {
		t.Errorf("expected warning in warnings buffer, got %q", warnings.String())
	}
}

func TestRetry_PassingTaskShowsRetryWarning(t *testing.T) {
	var buf syncBuffer
	scope := newTestScope(t).WithOutput(&buf, &buf)

	task := &fakeRetryableTask{
		name:     "task-retry-pass",
		failTill: 1,
		policy:   Backoff{0},
	}

	err := RunTask(t.Context(), scope, task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "=== task-retry-pass (ok,") {
		t.Errorf("expected status line in output:\n%s", out)
	}
	if !strings.Contains(out, "task failed, retrying level=warning task=task-retry-pass attempt=1") {
		t.Errorf("expected retry warning line in output:\n%s", out)
	}
}

func TestBufferHandler_VerbosityLevels(t *testing.T) {
	tests := []struct {
		vLevel int
		want   []string
		omit   []string
	}{
		{
			vLevel: 0,
			want:   []string{"error line", "warn line", "info line"},
			omit:   []string{"debug line", "fine line", "finer line", "finest line", "klog v1", "klog v2", "klog v3", "klog v4"},
		},
		{
			vLevel: 1,
			want:   []string{"error line", "warn line", "info line", "debug line", "klog v1"},
			omit:   []string{"fine line", "finer line", "finest line", "klog v2", "klog v3", "klog v4"},
		},
		{
			vLevel: 2,
			want:   []string{"error line", "warn line", "info line", "debug line", "fine line", "klog v1", "klog v2"},
			omit:   []string{"finer line", "finest line", "klog v3", "klog v4"},
		},
		{
			vLevel: 3,
			want:   []string{"error line", "warn line", "info line", "debug line", "fine line", "finer line", "klog v1", "klog v2", "klog v3"},
			omit:   []string{"finest line", "klog v4"},
		},
		{
			vLevel: 4,
			want:   []string{"error line", "warn line", "info line", "debug line", "fine line", "finer line", "finest line", "klog v1", "klog v2", "klog v3", "klog v4"},
			omit:   nil,
		},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("v=%d", tc.vLevel), func(t *testing.T) {
			fs := flag.NewFlagSet("klog", flag.ContinueOnError)
			klog.InitFlags(fs)
			if err := fs.Set("v", strconv.Itoa(tc.vLevel)); err != nil {
				t.Fatalf("setting -v: %v", err)
			}
			defer fs.Set("v", "0")

			var buf bytes.Buffer
			logger := NewLogger(&buf)
			ctx := scoped.WithLog(t.Context(), logger)

			logger.Error("error line")
			logger.Warn("warn line")
			logger.Info("info line")
			logger.Debug("debug line")
			logger.Fine("fine line")
			logger.Finer("finer line")
			logger.Finest("finest line")

			klog.FromContext(ctx).V(1).Info("klog v1")
			klog.FromContext(ctx).V(2).Info("klog v2")
			klog.FromContext(ctx).V(3).Info("klog v3")
			klog.FromContext(ctx).V(4).Info("klog v4")

			out := buf.String()
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("at -v=%d, expected %q in output:\n%s", tc.vLevel, w, out)
				}
			}
			for _, o := range tc.omit {
				if strings.Contains(out, o) {
					t.Errorf("at -v=%d, expected %q NOT in output:\n%s", tc.vLevel, o, out)
				}
			}
		})
	}
}
