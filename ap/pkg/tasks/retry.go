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

type bufferSink struct {
	writer io.Writer
	parent logr.LogSink
	name   string
	values []any
}

var _ logr.LogSink = bufferSink{}
var _ logr.CallDepthLogSink = bufferSink{}

func (s bufferSink) Init(info logr.RuntimeInfo) {}

func (s bufferSink) Enabled(level int) bool {
	if s.parent != nil {
		return s.parent.Enabled(level)
	}
	return klog.V(klog.Level(level)).Enabled()
}

func (s bufferSink) Info(level int, msg string, keysAndValues ...any) {
	allKeysAndValues := make([]any, 0, len(s.values)+len(keysAndValues))
	allKeysAndValues = append(allKeysAndValues, s.values...)
	allKeysAndValues = append(allKeysAndValues, keysAndValues...)
	s.writeLine(msg, allKeysAndValues)
}

func (s bufferSink) Error(err error, msg string, keysAndValues ...any) {
	allKeysAndValues := make([]any, 0, 2+len(s.values)+len(keysAndValues))
	allKeysAndValues = append(allKeysAndValues, "error", err)
	allKeysAndValues = append(allKeysAndValues, s.values...)
	allKeysAndValues = append(allKeysAndValues, keysAndValues...)
	s.writeLine(msg, allKeysAndValues)
}

func (s bufferSink) WithValues(keysAndValues ...any) logr.LogSink {
	newValues := make([]any, 0, len(s.values)+len(keysAndValues))
	newValues = append(newValues, s.values...)
	newValues = append(newValues, keysAndValues...)
	return bufferSink{
		writer: s.writer,
		parent: s.parent,
		name:   s.name,
		values: newValues,
	}
}

func (s bufferSink) WithName(name string) logr.LogSink {
	if s.name != "" {
		name = s.name + "/" + name
	}
	return bufferSink{
		writer: s.writer,
		parent: s.parent,
		name:   name,
		values: s.values,
	}
}

func (s bufferSink) WithCallDepth(depth int) logr.LogSink {
	return s
}

func (s bufferSink) writeLine(msg string, keysAndValues []any) {
	var parts []string
	if msg != "" {
		parts = append(parts, msg)
	}
	for i := 0; i < len(keysAndValues); i += 2 {
		key := fmt.Sprint(keysAndValues[i])
		var value any
		if i+1 < len(keysAndValues) {
			value = keysAndValues[i+1]
		} else {
			value = "<no-value>"
		}
		parts = append(parts, fmt.Sprintf("%s=%s", key, formatValue(value)))
	}
	line := strings.Join(parts, " ")
	if s.name != "" {
		writef(s.writer, "%s: %s\n", s.name, line)
	} else {
		writef(s.writer, "%s\n", line)
	}
}

func formatValue(value any) string {
	formatted := fmt.Sprint(value)
	if strings.Contains(formatted, " ") {
		return fmt.Sprintf("%q", formatted)
	}
	return formatted
}

// NewLogger returns a plain logger that formats log entries as 'message key=value'.
func NewLogger(writer io.Writer) logr.Logger {
	return logr.New(bufferSink{
		writer: writer,
	})
}

func newBufferLogger(writer io.Writer, parent logr.Logger) logr.Logger {
	return logr.New(bufferSink{
		writer: writer,
		parent: parent.GetSink(),
	})
}

func sanitizePathComponent(name string) string {
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, name)
	if sanitized == ".." || sanitized == "" {
		return "_"
	}
	return sanitized
}

func deriveTaskLogPath(scope *TaskContext, task Task) string {
	components := []string{scope.artifactsDir, "tasks"}
	for _, parent := range scope.parents {
		components = append(components, sanitizePathComponent(parent))
	}
	dir := filepath.Join(components...)
	baseName := sanitizePathComponent(task.GetName())

	scope.runState.mutex.Lock()
	defer scope.runState.mutex.Unlock()

	for i := 1; ; i++ {
		candidate := filepath.Join(dir, baseName+".log")
		if i > 1 {
			candidate = filepath.Join(dir, fmt.Sprintf("%s-%d.log", baseName, i))
		}
		if !scope.runState.usedPaths[candidate] {
			scope.runState.usedPaths[candidate] = true
			return candidate
		}
	}
}

func runLeafTask(ctx context.Context, scope *TaskContext, task Task) error {
	outputBuffer := &syncBuffer{}
	outputWriter := scope.Stdout()

	var taskStdout, taskStderr io.Writer
	if scope.PrintOutput() {
		taskStdout = io.MultiWriter(outputWriter, outputBuffer)
		taskStderr = io.MultiWriter(scope.Stderr(), outputBuffer)
	} else {
		taskStdout = outputBuffer
		taskStderr = outputBuffer
	}

	taskScope := scope.WithOutput(taskStdout, taskStderr)
	taskLog := newBufferLogger(taskScope.Stderr(), scoped.Log(ctx))
	taskCtx := scoped.WithLog(ctx, taskLog)

	var taskDirectory string
	if scope.ArtifactsDir() != "" {
		logFile := deriveTaskLogPath(scope, task)
		taskDirectory = filepath.Dir(logFile)
		if err := os.MkdirAll(taskDirectory, 0755); err != nil {
			return fmt.Errorf("creating task artifacts directory %s: %w", taskDirectory, err)
		}
		taskScope.taskLogFile = logFile
	} else {
		temporaryDirectory, err := os.MkdirTemp("", "ap-task-*")
		if err != nil {
			return fmt.Errorf("creating temporary task directory: %w", err)
		}
		defer func() {
			if removeErr := os.RemoveAll(temporaryDirectory); removeErr != nil {
				scoped.Log(ctx).Info("failed to remove temporary task directory", "level", "warning", "error", removeErr)
			}
		}()
		taskDirectory = temporaryDirectory
	}
	taskScope.taskDir = taskDirectory

	start := time.Now()
	err := runTaskWithRetry(taskCtx, taskScope, task)
	duration := time.Since(start)

	status := "ok"
	if err != nil {
		status = "FAILED"
	}

	runnerMu.Lock()
	defer runnerMu.Unlock()

	writef(outputWriter, "=== %s (%s, %.1fs)\n", task.GetName(), status, duration.Seconds())
	output := outputBuffer.Bytes()
	if !scope.PrintOutput() && (err != nil || scoped.Log(ctx).V(1).Enabled()) {
		if len(output) > 0 {
			if _, writeErr := outputWriter.Write(output); writeErr != nil {
				scoped.Log(ctx).V(2).Info("failed to write task output", "error", writeErr)
			}
			if !bytes.HasSuffix(output, []byte("\n")) {
				writef(outputWriter, "\n")
			}
		}
	}

	if taskScope.taskLogFile != "" {
		content := append([]byte(task.GetName()+"\n"), output...)
		if writeErr := os.WriteFile(taskScope.taskLogFile, content, 0644); writeErr != nil {
			scoped.Log(ctx).Info("failed to write task artifact log", "level", "warning", "file", taskScope.taskLogFile, "error", writeErr)
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
		scoped.Log(ctx).Info("task failed, retrying", "level", "warning", "task", task.GetName(), "attempt", attempt+1, "error", err, "wait", wait)
		select {
		case <-ctx.Done():
			return fmt.Errorf("task %s failed: %w", task.GetName(), ctx.Err())
		case <-time.After(wait):
		}
	}
}
