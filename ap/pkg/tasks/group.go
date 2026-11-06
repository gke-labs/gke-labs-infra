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

	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
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
		scoped.Log(context.Background()).V(2).Info("failed to write output", "error", err)
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
		g.writeFailureSummary(scope, failedNames)
		return errors.Join(errs...)
	}
	return nil
}

func (g *Group) writeFailureSummary(scope *TaskContext, failedNames []string) {
	if len(failedNames) > 0 && len(g.Tasks) > 1 {
		writef(scope.Stdout(), "%s: %d of %d failed: %s\n", g.Name, len(failedNames), len(g.Tasks), strings.Join(failedNames, ", "))
	}
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
		err error
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

				err := RunTask(runCtx, scope, item.task)

				resultsMu.Lock()
				results[item.index] = &taskResult{err: err}
				resultsMu.Unlock()

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

	var errs []error
	var failedNames []string

	for i, res := range results {
		if res == nil {
			continue
		}
		if res.err != nil {
			if !g.ContinueOnError && errors.Is(res.err, context.Canceled) && !errors.Is(firstErr, context.Canceled) {
				continue
			}
			errs = append(errs, res.err)
			failedNames = append(failedNames, strings.TrimSuffix(g.Tasks[i].GetName(), "-check"))
		}
	}

	g.writeFailureSummary(scope, failedNames)

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
				scope.Log().V(2).Info("failed to save sourcetree hash cache", "error", saveErr)
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
