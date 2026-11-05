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
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func writef(w io.Writer, format string, args ...any) {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		klog.V(2).Infof("failed to write output: %v", err)
	}
}

// Group represents a collection of tasks.
type Group struct {
	Name            string
	Tasks           []Task
	ContinueOnError bool
	Parallel        bool
	maxWorkers      int
}

func (g *Group) Run(ctx context.Context, scope *TaskContext) error {
	if !g.Parallel {
		return g.runSequential(ctx, scope)
	}
	return g.runParallel(ctx, scope)
}

func (g *Group) runSequential(ctx context.Context, scope *TaskContext) error {
	var errs []error
	var failedNames []string
	for _, t := range g.Tasks {
		if err := RunTask(ctx, scope, t); err != nil {
			if !g.ContinueOnError {
				return err
			}
			errs = append(errs, err)
			failedNames = append(failedNames, strings.TrimSuffix(t.GetName(), "-check"))
		}
	}
	if len(errs) > 0 {
		if len(g.Tasks) > 1 {
			klog.Errorf("%s: %d of %d failed: %s", g.Name, len(failedNames), len(g.Tasks), strings.Join(failedNames, ", "))
		}
		return errors.Join(errs...)
	}
	return nil
}

func (g *Group) runParallel(ctx context.Context, scope *TaskContext) error {
	if len(g.Tasks) == 0 {
		return nil
	}

	limit := runtime.NumCPU()
	if g.maxWorkers > 0 {
		limit = g.maxWorkers
	}
	if limit < 1 {
		limit = 1
	}
	if limit > len(g.Tasks) {
		limit = len(g.Tasks)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type taskItem struct {
		index int
		task  Task
	}

	type taskResult struct {
		index    int
		task     Task
		duration time.Duration
		output   []byte
		err      error
	}

	taskCh := make(chan taskItem, len(g.Tasks))
	for i, t := range g.Tasks {
		taskCh <- taskItem{index: i, task: t}
	}
	close(taskCh)

	results := make([]*taskResult, len(g.Tasks))
	var resultsMu sync.Mutex

	var firstErrMu sync.Mutex
	var firstErr error

	var printMu sync.Mutex
	out := scope.Stdout()

	var wg sync.WaitGroup
	for w := 0; w < limit; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range taskCh {
				select {
				case <-runCtx.Done():
					return
				default:
				}

				buf := &syncBuffer{}
				taskScope := scope.WithOutput(buf, buf)

				start := time.Now()
				err := RunTask(runCtx, taskScope, item.task)
				duration := time.Since(start)

				output := buf.Bytes()
				res := &taskResult{
					index:    item.index,
					task:     item.task,
					duration: duration,
					output:   output,
					err:      err,
				}

				resultsMu.Lock()
				results[item.index] = res
				resultsMu.Unlock()

				status := "ok"
				if err != nil {
					status = "FAILED"
				}

				printMu.Lock()
				writef(out, "=== %s (%s, %.1fs)\n", item.task.GetName(), status, duration.Seconds())
				if len(output) > 0 {
					if _, err := out.Write(output); err != nil {
						klog.V(2).Infof("failed to write task output: %v", err)
					}
					if !bytes.HasSuffix(output, []byte("\n")) {
						writef(out, "\n")
					}
				}
				printMu.Unlock()

				if err != nil {
					firstErrMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					firstErrMu.Unlock()

					if !g.ContinueOnError {
						cancel()
					}
				}
			}
		}()
	}

	wg.Wait()

	var failedResults []*taskResult
	var errs []error
	var failedNames []string

	for _, res := range results {
		if res == nil {
			continue
		}
		if res.err != nil {
			if !g.ContinueOnError && errors.Is(res.err, context.Canceled) && !errors.Is(firstErr, context.Canceled) {
				continue
			}
			failedResults = append(failedResults, res)
			errs = append(errs, res.err)
			failedNames = append(failedNames, strings.TrimSuffix(res.task.GetName(), "-check"))
		}
	}

	if len(failedResults) > 0 {
		writef(out, "\n=== FAILURE SUMMARY ===\n")
		for _, res := range failedResults {
			writef(out, "=== %s (FAILED, %.1fs) ===\n", res.task.GetName(), res.duration.Seconds())
			if len(res.output) > 0 {
				if _, err := out.Write(res.output); err != nil {
					klog.V(2).Infof("failed to write failure summary output: %v", err)
				}
				if !bytes.HasSuffix(res.output, []byte("\n")) {
					writef(out, "\n")
				}
			}
		}
		if len(g.Tasks) > 1 {
			klog.Errorf("%s: %d of %d failed: %s", g.Name, len(failedNames), len(g.Tasks), strings.Join(failedNames, ", "))
		}
	}

	if !g.ContinueOnError {
		return firstErr
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (g *Group) GetName() string {
	return g.Name
}

func (g *Group) GetChildren() []Task {
	return g.Tasks
}

// WithChildren returns a shallow copy of the group with new children tasks.
func (g *Group) WithChildren(children []Task) Task {
	cp := *g
	cp.Tasks = children
	return &cp
}

var _ Parent = (*Group)(nil)

// RunOptions holds options for running tasks.
type RunOptions struct {
	DryRun          bool
	ContinueOnError bool
}

// Run executes a list of tasks.
func Run(ctx context.Context, scope *TaskContext, ts []Task, opts RunOptions) error {
	if opts.DryRun {
		out := scope.Stdout()
		for _, task := range ts {
			PrintTree(out, task, 0)
		}
		return nil
	}
	defer func() {
		if scope != nil && scope.SourceTree() != nil {
			if saveErr := scope.SourceTree().Save(); saveErr != nil {
				klog.V(2).Infof("failed to save sourcetree hash cache: %v", saveErr)
			}
		}
	}()
	var errs []error
	for _, task := range ts {
		if err := RunTask(ctx, scope, task); err != nil {
			if !opts.ContinueOnError {
				return err
			}
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
