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
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
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

type bufferHandler struct {
	writer   io.Writer
	warnings *syncBuffer
	prefix   string
	attrs    []slog.Attr
}

var _ slog.Handler = (*bufferHandler)(nil)

func (h *bufferHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if level >= slog.LevelInfo {
		return true
	}
	return klog.V(klog.Level(-level)).Enabled()
}

func (h *bufferHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	clone := *h
	clone.attrs = append(slices.Clip(h.attrs), attrs...)
	return &clone
}

func (h *bufferHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	if clone.prefix != "" {
		clone.prefix = clone.prefix + "/" + name
	} else {
		clone.prefix = name
	}
	return &clone
}

func (h *bufferHandler) Handle(ctx context.Context, r slog.Record) error {
	var parts []string
	if r.Message != "" {
		parts = append(parts, r.Message)
	}

	if r.Level == slog.LevelWarn {
		parts = append(parts, "level=warning")
	}

	for _, a := range h.attrs {
		appendAttr(&parts, a)
	}

	r.Attrs(func(a slog.Attr) bool {
		appendAttr(&parts, a)
		return true
	})

	line := strings.Join(parts, " ")
	if h.prefix != "" {
		line = h.prefix + ": " + line
	}

	writef(h.writer, "%s\n", line)
	if h.warnings != nil && r.Level == slog.LevelWarn {
		writef(h.warnings, "%s\n", line)
	}
	return nil
}

func appendAttr(parts *[]string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		for _, child := range a.Value.Group() {
			appendAttr(parts, slog.Attr{
				Key:   a.Key + "." + child.Key,
				Value: child.Value,
			})
		}
		return
	}
	key := a.Key
	val := formatValue(a.Value.Any())
	*parts = append(*parts, fmt.Sprintf("%s=%s", key, val))
}

func formatValue(value any) string {
	formatted := fmt.Sprint(value)
	if strings.Contains(formatted, " ") {
		return fmt.Sprintf("%q", formatted)
	}
	return formatted
}

// NewHandler returns an slog.Handler that formats log entries as 'message key=value'.
func NewHandler(writer io.Writer) slog.Handler {
	return &bufferHandler{
		writer: writer,
	}
}

// NewLogger returns a plain logger that formats log entries as 'message key=value'.
func NewLogger(writer io.Writer) scoped.Logger {
	return scoped.NewLogger(NewHandler(writer))
}

func newBufferLogger(writer io.Writer, warnings *syncBuffer) scoped.Logger {
	return scoped.NewLogger(&bufferHandler{
		writer:   writer,
		warnings: warnings,
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
	warningsBuffer := &syncBuffer{}
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
	taskLog := newBufferLogger(taskScope.Stderr(), warningsBuffer)
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
				scoped.Log(ctx).Warn("failed to remove temporary task directory", "error", removeErr)
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
	if !scope.PrintOutput() && (err != nil || klog.V(1).Enabled()) {
		if len(output) > 0 {
			if _, writeErr := outputWriter.Write(output); writeErr != nil {
				taskLog.Fine("failed to write task output", "error", writeErr)
			}
			if !bytes.HasSuffix(output, []byte("\n")) {
				writef(outputWriter, "\n")
			}
		}
	} else if !scope.PrintOutput() {
		warnings := warningsBuffer.Bytes()
		if len(warnings) > 0 {
			if _, writeErr := outputWriter.Write(warnings); writeErr != nil {
				taskLog.Fine("failed to write task warnings", "error", writeErr)
			}
			if !bytes.HasSuffix(warnings, []byte("\n")) {
				writef(outputWriter, "\n")
			}
		}
	}

	if taskScope.taskLogFile != "" {
		content := append([]byte(task.GetName()+"\n"), output...)
		if writeErr := os.WriteFile(taskScope.taskLogFile, content, 0644); writeErr != nil {
			scoped.Log(ctx).Warn("failed to write task artifact log", "file", taskScope.taskLogFile, "error", writeErr)
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
		scoped.Log(ctx).Warn("task failed, retrying", "task", task.GetName(), "attempt", attempt+1, "error", err, "wait", wait)
		select {
		case <-ctx.Done():
			return fmt.Errorf("task %s failed: %w", task.GetName(), ctx.Err())
		case <-time.After(wait):
		}
	}
}
