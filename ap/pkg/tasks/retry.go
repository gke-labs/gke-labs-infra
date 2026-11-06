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
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
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

type bufferSink struct {
	logr.LogSink
	parent logr.LogSink
}

var _ logr.LogSink = bufferSink{}
var _ logr.CallDepthLogSink = bufferSink{}

func (s bufferSink) Enabled(level int) bool {
	if s.parent != nil {
		return s.parent.Enabled(level)
	}
	return false
}

func (s bufferSink) WithValues(keysAndValues ...any) logr.LogSink {
	return bufferSink{
		LogSink: s.LogSink.WithValues(keysAndValues...),
		parent:  s.parent,
	}
}

func (s bufferSink) WithName(name string) logr.LogSink {
	return bufferSink{
		LogSink: s.LogSink.WithName(name),
		parent:  s.parent,
	}
}

func (s bufferSink) WithCallDepth(depth int) logr.LogSink {
	if cd, ok := s.LogSink.(logr.CallDepthLogSink); ok {
		return bufferSink{
			LogSink: cd.WithCallDepth(depth),
			parent:  s.parent,
		}
	}
	return s
}

func newBufferLogger(w io.Writer, parent logr.Logger) logr.Logger {
	empty := ""
	fnLog := funcr.New(func(prefix, args string) {
		if prefix != "" {
			writef(w, "%s: %s\n", prefix, args)
		} else {
			writef(w, "%s\n", args)
		}
	}, funcr.Options{
		LogInfoLevel: &empty,
	})
	return logr.New(bufferSink{
		LogSink: fnLog.GetSink(),
		parent:  parent.GetSink(),
	})
}

func sanitizeLogTaskName(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	return name
}

func runLeafTask(ctx context.Context, scope *TaskContext, task Task) error {
	buf := &syncBuffer{}
	taskScope := scope.WithOutput(buf, buf)
	taskLog := newBufferLogger(taskScope.Stderr(), scope.Log())
	taskScope = taskScope.WithLog(taskLog)
	ctx = scoped.WithLog(ctx, taskLog)
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
	output := buf.Bytes()
	if err != nil || scope.Log().V(1).Enabled() || scope.PrintOutput() {
		if len(output) > 0 {
			if _, writeErr := out.Write(output); writeErr != nil {
				scope.Log().V(2).Info("failed to write task output", "error", writeErr)
			}
			if !bytes.HasSuffix(output, []byte("\n")) {
				writef(out, "\n")
			}
		}
	}

	if artifactsDir := scope.ArtifactsDir(); artifactsDir != "" {
		tasksDir := filepath.Join(artifactsDir, "tasks")
		if mkdirErr := os.MkdirAll(tasksDir, 0755); mkdirErr != nil {
			scope.Log().Info("failed to create artifacts tasks directory", "level", "warning", "dir", tasksDir, "error", mkdirErr)
		} else {
			safeName := sanitizeLogTaskName(task.GetName())
			logFile := filepath.Join(tasksDir, safeName+".log")
			if writeErr := os.WriteFile(logFile, output, 0644); writeErr != nil {
				scope.Log().Info("failed to write task artifact log", "level", "warning", "file", logFile, "error", writeErr)
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
		scope.Log().Info("task failed, retrying", "level", "warning", "task", task.GetName(), "attempt", attempt+1, "error", err, "wait", wait)
		select {
		case <-ctx.Done():
			return fmt.Errorf("task %s failed: %w", task.GetName(), ctx.Err())
		case <-time.After(wait):
		}
	}
}
