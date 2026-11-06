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
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"k8s.io/klog/v2"
)

func TestFindTaskScripts(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ap-tasks-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	tasksDir := filepath.Join(tmpDir, "dev", "tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatal(err)
	}

	scriptPath := filepath.Join(tasksDir, "build-foo")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho foo"), 0755); err != nil {
		t.Fatal(err)
	}

	r, err := sourcetree.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	foundTasks, err := FindTaskScripts(r, tmpDir, WithPrefix("build-"))
	if err != nil {
		t.Fatalf("FindTaskScripts() error = %v", err)
	}

	if len(foundTasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(foundTasks))
	}

	ts, ok := foundTasks[0].(*TaskScript)
	if !ok {
		t.Fatalf("expected *TaskScript, got %T", foundTasks[0])
	}

	if ts.Name != "build-foo" {
		t.Errorf("expected name build-foo, got %s", ts.Name)
	}

	if ts.Root != tmpDir {
		t.Errorf("expected Root %s, got %s", tmpDir, ts.Root)
	}

	if _, err := FindTaskScripts(nil, tmpDir); err == nil {
		t.Errorf("expected error when repo is nil, got nil")
	}
}

func TestTaskContextSourceTree(t *testing.T) {
	tmpDir := t.TempDir()
	tree, err := sourcetree.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	scope := NewTaskContext(tmpDir, tmpDir, tree)
	if scope.SourceTree() != tree {
		t.Fatalf("SourceTree() = %v, want %v", scope.SourceTree(), tree)
	}
	if scope.RepoRoot() != tmpDir {
		t.Fatalf("RepoRoot() = %q, want %q", scope.RepoRoot(), tmpDir)
	}
	if scope.Dir() != tmpDir {
		t.Fatalf("Dir() = %q, want %q", scope.Dir(), tmpDir)
	}
	if scope.Stdout() != os.Stdout {
		t.Fatalf("Stdout() = %v, want %v", scope.Stdout(), os.Stdout)
	}
	if scope.Stderr() != os.Stderr {
		t.Fatalf("Stderr() = %v, want %v", scope.Stderr(), os.Stderr)
	}

	// NewTaskContext must panic if tree is nil
	defer func() {
		r := recover()
		if r == nil {
			t.Errorf("expected panic when tree is nil, got nil")
		}
	}()
	_ = NewTaskContext(tmpDir, tmpDir, nil)
}

func TestGroupRun_ContinueOnError(t *testing.T) {
	errA := errors.New("error A")
	errC := errors.New("error C")

	var ranA, ranB, ranC bool

	group := &Group{
		Name:            "go-lint-testmodule",
		ContinueOnError: true,
		Tasks: []Task{
			&FuncTask{
				Name: "check-a",
				RunFn: func(ctx context.Context, scope *TaskContext) error {
					ranA = true
					return errA
				},
			},
			&FuncTask{
				Name: "check-b",
				RunFn: func(ctx context.Context, scope *TaskContext) error {
					ranB = true
					return nil
				},
			},
			&FuncTask{
				Name: "check-c",
				RunFn: func(ctx context.Context, scope *TaskContext) error {
					ranC = true
					return errC
				},
			},
		},
	}

	ctx := t.Context()
	tree, err := sourcetree.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scope := NewTaskContext(t.TempDir(), t.TempDir(), tree)
	err = group.Run(ctx, scope)
	if err == nil {
		t.Fatalf("expected error from Group.Run(), got nil")
	}

	// Assert all children ran
	if !ranA {
		t.Errorf("expected child A to run")
	}
	if !ranB {
		t.Errorf("expected child B to run")
	}
	if !ranC {
		t.Errorf("expected child C to run")
	}

	// Assert joined error contains each child's error
	if !errors.Is(err, errA) {
		t.Errorf("expected joined error to contain errA, got %v", err)
	}
	if !errors.Is(err, errC) {
		t.Errorf("expected joined error to contain errC, got %v", err)
	}
}

func TestGroupRun_FailFast(t *testing.T) {
	errA := errors.New("error A")
	errB := errors.New("error B")

	var ranA, ranB bool

	group := &Group{
		Name:            "build-images",
		ContinueOnError: false,
		Tasks: []Task{
			&FuncTask{
				Name: "task-a",
				RunFn: func(ctx context.Context, scope *TaskContext) error {
					ranA = true
					return errA
				},
			},
			&FuncTask{
				Name: "task-b",
				RunFn: func(ctx context.Context, scope *TaskContext) error {
					ranB = true
					return errB
				},
			},
		},
	}

	ctx := t.Context()
	tree, err := sourcetree.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scope := NewTaskContext(t.TempDir(), t.TempDir(), tree)
	err = group.Run(ctx, scope)
	if err == nil {
		t.Fatalf("expected error from Group.Run(), got nil")
	}

	if !ranA {
		t.Errorf("expected task A to run")
	}
	if ranB {
		t.Errorf("expected task B not to run when ContinueOnError is false")
	}
	if !errors.Is(err, errA) {
		t.Errorf("expected errA, got %v", err)
	}
}

func TestTasksRun_ContinueOnError(t *testing.T) {
	err1 := errors.New("err 1")
	err2 := errors.New("err 2")

	var ran1, ran2 bool
	ts := []Task{
		&FuncTask{
			Name: "t1",
			RunFn: func(ctx context.Context, scope *TaskContext) error {
				ran1 = true
				return err1
			},
		},
		&FuncTask{
			Name: "t2",
			RunFn: func(ctx context.Context, scope *TaskContext) error {
				ran2 = true
				return err2
			},
		},
	}

	ctx := t.Context()
	tree, err := sourcetree.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scope := NewTaskContext(t.TempDir(), t.TempDir(), tree)

	// Test fail-fast
	err = Run(ctx, scope, ts, RunOptions{ContinueOnError: false})
	if !ran1 || ran2 {
		t.Errorf("fail-fast: expected ran1=true, ran2=false, got ran1=%v, ran2=%v", ran1, ran2)
	}
	if !errors.Is(err, err1) {
		t.Errorf("expected err1, got %v", err)
	}

	// Test continue on error
	ran1, ran2 = false, false
	err = Run(ctx, scope, ts, RunOptions{ContinueOnError: true})
	if !ran1 || !ran2 {
		t.Errorf("continue-on-error: expected ran1=true, ran2=true, got ran1=%v, ran2=%v", ran1, ran2)
	}
	if !errors.Is(err, err1) || !errors.Is(err, err2) {
		t.Errorf("expected joined error with err1 and err2, got %v", err)
	}
}

func TestTaskScript_ResolvesApOnPath(t *testing.T) {
	tmpDir := t.TempDir()

	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	fakeAp := filepath.Join(binDir, "ap")
	if err := os.WriteFile(fakeAp, []byte("#!/bin/sh\necho \"fake-ap-version\"\n"), 0755); err != nil {
		t.Fatal(err)
	}

	scriptFile := filepath.Join(tmpDir, "test.sh")
	outputFile := filepath.Join(tmpDir, "output.txt")
	scriptContent := fmt.Sprintf("#!/bin/sh\nap > %q\n", outputFile)
	if err := os.WriteFile(scriptFile, []byte(scriptContent), 0755); err != nil {
		t.Fatal(err)
	}

	ts := &TaskScript{
		Name: "test-script",
		Path: scriptFile,
		Root: tmpDir,
	}

	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)
	t.Setenv("AP_EXECUTABLE", fakeAp)
	tree, err := sourcetree.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Run(t.Context(), NewTaskContext(tmpDir, tmpDir, tree)); err != nil {
		t.Fatalf("task run failed: %v", err)
	}

	out, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("failed to read output: %v", err)
	}
	if !strings.Contains(string(out), "fake-ap-version") {
		t.Errorf("unexpected output: %s", string(out))
	}
}

func TestTaskScript_ResolvesToolsOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinking is not supported on Windows")
	}

	tmpDir := t.TempDir()

	fakeAp := filepath.Join(t.TempDir(), "ap")
	fakeScript := "#!/bin/sh\necho \"fake-tool: $(basename \"$0\")\"\n"
	if err := os.WriteFile(fakeAp, []byte(fakeScript), 0755); err != nil {
		t.Fatal(err)
	}

	scriptFile := filepath.Join(tmpDir, "test.sh")
	outputFile := filepath.Join(tmpDir, "output.txt")
	scriptContent := fmt.Sprintf("#!/bin/sh\nkind >> %q\nkubectl >> %q\n", outputFile, outputFile)
	if err := os.WriteFile(scriptFile, []byte(scriptContent), 0755); err != nil {
		t.Fatal(err)
	}

	ts := &TaskScript{
		Name: "test-script",
		Path: scriptFile,
		Root: tmpDir,
	}

	t.Setenv("AP_EXECUTABLE", fakeAp)
	tree, err := sourcetree.Open(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Run(t.Context(), NewTaskContext(tmpDir, tmpDir, tree)); err != nil {
		t.Fatalf("task run failed: %v", err)
	}

	out, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("failed to read output: %v", err)
	}
	outStr := string(out)
	if !strings.Contains(outStr, "fake-tool: kind") {
		t.Errorf("expected output to contain 'fake-tool: kind', got: %s", outStr)
	}
	if !strings.Contains(outStr, "fake-tool: kubectl") {
		t.Errorf("expected output to contain 'fake-tool: kubectl', got: %s", outStr)
	}
}

func TestParallelGroup(t *testing.T) {
	// 1. Verify concurrency actually happens, bounded by the limit,
	// and output blocks are whole and in completion order.
	t.Run("ConcurrencyAndCompletionOrder", func(t *testing.T) {
		fs := flag.NewFlagSet("klog", flag.ContinueOnError)
		klog.InitFlags(fs)
		_ = fs.Set("v", "1")
		defer fs.Set("v", "0")

		const limit = 2
		var activeCount int64
		var maxActive int64

		startTask0 := make(chan struct{})
		startTask1 := make(chan struct{})
		allowTask1Finish := make(chan struct{})
		allowTask0Finish := make(chan struct{})
		allowTask2Finish := make(chan struct{})

		var outBuf syncBuffer
		tree, err := sourcetree.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		scope := NewTaskContext(t.TempDir(), t.TempDir(), tree).WithOutput(&outBuf, &outBuf)

		recordActive := func() func() {
			curr := atomic.AddInt64(&activeCount, 1)
			for {
				old := atomic.LoadInt64(&maxActive)
				if curr <= old || atomic.CompareAndSwapInt64(&maxActive, old, curr) {
					break
				}
			}
			return func() {
				atomic.AddInt64(&activeCount, -1)
			}
		}

		g := &Group{
			Name:       "test-parallel-group",
			Parallel:   true,
			maxWorkers: limit,
			Tasks: []Task{
				&FuncTask{
					Name: "task-0",
					RunFn: func(ctx context.Context, s *TaskContext) error {
						done := recordActive()
						defer done()
						close(startTask0)
						// Wait for task 1 to be active too
						<-startTask1
						// Wait for permission to finish (after task 1 has finished)
						<-allowTask0Finish
						fmt.Fprintln(s.Stdout(), "stdout line from task 0")
						fmt.Fprintln(s.Stderr(), "stderr line from task 0")
						return nil
					},
				},
				&FuncTask{
					Name: "task-1",
					RunFn: func(ctx context.Context, s *TaskContext) error {
						done := recordActive()
						defer done()
						// Wait for task 0 to start
						<-startTask0
						close(startTask1)
						<-allowTask1Finish
						fmt.Fprintln(s.Stdout(), "stdout line from task 1")
						fmt.Fprintln(s.Stderr(), "stderr line from task 1")
						return nil
					},
				},
				&FuncTask{
					Name: "task-2",
					RunFn: func(ctx context.Context, s *TaskContext) error {
						done := recordActive()
						defer done()
						<-allowTask2Finish
						fmt.Fprintln(s.Stdout(), "output from task 2")
						return nil
					},
				},
			},
		}

		// Run in background goroutine while we orchestrate completion order
		errCh := make(chan error, 1)
		go func() {
			errCh <- g.Run(t.Context(), scope)
		}()

		// Both task 0 and task 1 have started
		<-startTask1

		// Verify that at least 2 tasks ran concurrently
		// and that task 2 did NOT start while tasks 0 and 1 were active (bounded by limit=2)
		if got := atomic.LoadInt64(&activeCount); got != 2 {
			t.Errorf("expected activeCount == 2 while task 0 and task 1 are running, got %d", got)
		}

		// Allow task 1 to finish first!
		close(allowTask1Finish)
		// Wait briefly to ensure task 1 completes and prints before task 0
		time.Sleep(20 * time.Millisecond)

		// Allow task 0 to finish second!
		close(allowTask0Finish)
		// Wait briefly to ensure task 0 completes and prints before task 2
		time.Sleep(20 * time.Millisecond)

		// Allow task 2 to finish third!
		close(allowTask2Finish)

		if err := <-errCh; err != nil {
			t.Fatalf("unexpected group run error: %v", err)
		}

		// Assert concurrency occurred and was bounded by limit
		if maxActive != limit {
			t.Errorf("expected maxActive == %d, got %d", limit, maxActive)
		}

		outStr := outBuf.String()

		// Verify output blocks are whole and in completion order:
		// task-1 must appear before task-0, because task-1 completed first!
		idx1 := strings.Index(outStr, "=== task-1 (ok,")
		idx0 := strings.Index(outStr, "=== task-0 (ok,")
		idx2 := strings.Index(outStr, "=== task-2 (ok,")

		if idx1 == -1 || idx0 == -1 || idx2 == -1 {
			t.Fatalf("missing expected task headers in output:\n%s", outStr)
		}

		if !(idx1 < idx0 && idx0 < idx2) {
			t.Errorf("expected completion order task-1 < task-0 < task-2, got indices task-1:%d, task-0:%d, task-2:%d in:\n%s",
				idx1, idx0, idx2, outStr)
		}

		// Verify task-1 block is whole (header followed by its stdout/stderr before any other task header)
		task1Block := outStr[idx1:idx0]
		if !strings.Contains(task1Block, "stdout line from task 1") || !strings.Contains(task1Block, "stderr line from task 1") {
			t.Errorf("task-1 block is not whole:\n%s", task1Block)
		}

		// Verify task-0 block is whole
		task0Block := outStr[idx0:idx2]
		if !strings.Contains(task0Block, "stdout line from task 0") || !strings.Contains(task0Block, "stderr line from task 0") {
			t.Errorf("task-0 block is not whole:\n%s", task0Block)
		}
	})

	// 2. First-error cancellation without ContinueOnError
	t.Run("FirstErrorCancellation", func(t *testing.T) {
		const limit = 2
		errFail := errors.New("boom failure")
		task1Started := make(chan struct{})
		var task1Cancelled atomic.Bool
		var task2Ran atomic.Bool

		var outBuf syncBuffer
		tree, err := sourcetree.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		scope := NewTaskContext(t.TempDir(), t.TempDir(), tree).WithOutput(&outBuf, &outBuf)

		g := &Group{
			Name:            "test-cancel-group",
			Parallel:        true,
			ContinueOnError: false,
			maxWorkers:      limit,
			Tasks: []Task{
				&FuncTask{
					Name: "task-0",
					RunFn: func(ctx context.Context, s *TaskContext) error {
						// Wait for task 1 to be actively running
						<-task1Started
						fmt.Fprintln(s.Stderr(), "task-0 failed details")
						return errFail
					},
				},
				&FuncTask{
					Name: "task-1",
					RunFn: func(ctx context.Context, s *TaskContext) error {
						close(task1Started)
						// Wait for context cancellation
						<-ctx.Done()
						task1Cancelled.Store(true)
						return ctx.Err()
					},
				},
				&FuncTask{
					Name: "task-2",
					RunFn: func(ctx context.Context, s *TaskContext) error {
						task2Ran.Store(true)
						return nil
					},
				},
			},
		}

		err = g.Run(t.Context(), scope)
		if !errors.Is(err, errFail) {
			t.Fatalf("expected first error errFail, got %v", err)
		}

		if !task1Cancelled.Load() {
			t.Errorf("expected task-1 to receive context cancellation")
		}
		if task2Ran.Load() {
			t.Errorf("expected queued task-2 not to run after cancellation")
		}

		outStr := outBuf.String()
		// Must contain task-0 completion block with FAILED
		if !strings.Contains(outStr, "=== task-0 (FAILED,") {
			t.Errorf("expected '=== task-0 (FAILED,' in output:\n%s", outStr)
		}
		// Must contain summary line with task-0
		if !strings.Contains(outStr, "test-cancel-group: 1 of 3 failed: task-0") {
			t.Errorf("expected failed task summary line in output:\n%s", outStr)
		}
		if !strings.Contains(outStr, "task-0 failed details") {
			t.Errorf("expected task-0 failed details in output:\n%s", outStr)
		}
	})

	// 3. ContinueOnError runs all tasks and joins errors
	t.Run("ContinueOnError", func(t *testing.T) {
		errA := errors.New("error A")
		errC := errors.New("error C")

		var ranA, ranB, ranC atomic.Bool
		var outBuf syncBuffer
		tree, err := sourcetree.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		scope := NewTaskContext(t.TempDir(), t.TempDir(), tree).WithOutput(&outBuf, &outBuf)

		g := &Group{
			Name:            "test-continue-group",
			Parallel:        true,
			ContinueOnError: true,
			Tasks: []Task{
				&FuncTask{
					Name: "task-a",
					RunFn: func(ctx context.Context, s *TaskContext) error {
						ranA.Store(true)
						fmt.Fprintln(s.Stderr(), "task-a error output")
						return errA
					},
				},
				&FuncTask{
					Name: "task-b",
					RunFn: func(ctx context.Context, s *TaskContext) error {
						ranB.Store(true)
						fmt.Fprintln(s.Stdout(), "task-b ok output")
						return nil
					},
				},
				&FuncTask{
					Name: "task-c",
					RunFn: func(ctx context.Context, s *TaskContext) error {
						ranC.Store(true)
						fmt.Fprintln(s.Stderr(), "task-c error output")
						return errC
					},
				},
			},
		}

		err = g.Run(t.Context(), scope)
		if err == nil {
			t.Fatalf("expected error from g.Run(), got nil")
		}

		if !ranA.Load() || !ranB.Load() || !ranC.Load() {
			t.Errorf("expected all tasks to run, got ranA=%v, ranB=%v, ranC=%v", ranA.Load(), ranB.Load(), ranC.Load())
		}

		if !errors.Is(err, errA) || !errors.Is(err, errC) {
			t.Errorf("expected joined error containing errA and errC, got %v", err)
		}

		outStr := outBuf.String()
		if !strings.Contains(outStr, "test-continue-group: 2 of 3 failed: task-a, task-c") {
			t.Errorf("expected failed task summary line in output:\n%s", outStr)
		}
		if !strings.Contains(outStr, "task-a error output") || !strings.Contains(outStr, "task-c error output") {
			t.Errorf("expected failed tasks output in output:\n%s", outStr)
		}
		if strings.Contains(outStr, "task-b ok output") {
			t.Errorf("expected passing task output to be omitted at default verbosity, got:\n%s", outStr)
		}
	})
}

func TestTaskOutputPolicy(t *testing.T) {
	newScope := func(t *testing.T, out *syncBuffer) *TaskContext {
		tree, err := sourcetree.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return NewTaskContext(t.TempDir(), t.TempDir(), tree).WithOutput(out, out)
	}

	makeTasks := func() (Task, Task) {
		pass := &FuncTask{
			Name: "task-pass",
			RunFn: func(ctx context.Context, s *TaskContext) error {
				fmt.Fprintln(s.Stdout(), "passing task text")
				return nil
			},
		}
		fail := &FuncTask{
			Name: "task-fail",
			RunFn: func(ctx context.Context, s *TaskContext) error {
				fmt.Fprintln(s.Stdout(), "failing task text")
				return errors.New("boom")
			},
		}
		return pass, fail
	}

	normalizeLines := func(s string) []string {
		var lines []string
		for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
			l = strings.TrimSpace(l)
			if l == "" {
				continue
			}
			if strings.HasPrefix(l, "=== ") && strings.HasSuffix(l, ")") {
				if idx := strings.LastIndex(l, ", "); idx != -1 {
					l = l[:idx] + ")"
				}
			}
			lines = append(lines, l)
		}
		sort.Strings(lines)
		return lines
	}

	// Sequential Group of two FuncTasks writing to tc.Stdout(), one failing;
	// at default verbosity passing task text is absent, failing task text is present.
	var seqBuf syncBuffer
	seqScope := newScope(t, &seqBuf)
	pass1, fail1 := makeTasks()
	seqGroup := &Group{
		Name:            "test-output-policy",
		ContinueOnError: true,
		Tasks:           []Task{pass1, fail1},
	}
	_ = seqGroup.Run(t.Context(), seqScope)
	seqOut := seqBuf.String()

	if strings.Contains(seqOut, "passing task text") {
		t.Errorf("expected passing task text to be absent at default verbosity, got:\n%s", seqOut)
	}
	if !strings.Contains(seqOut, "=== task-pass (ok,") {
		t.Errorf("expected passing task header in output, got:\n%s", seqOut)
	}
	if !strings.Contains(seqOut, "=== task-fail (FAILED,") {
		t.Errorf("expected failing task header in output, got:\n%s", seqOut)
	}
	if !strings.Contains(seqOut, "failing task text") {
		t.Errorf("expected failing task text to be present in output, got:\n%s", seqOut)
	}

	// The same group with Parallel: true yields the same set of lines.
	var parBuf syncBuffer
	parScope := newScope(t, &parBuf)
	pass2, fail2 := makeTasks()
	parGroup := &Group{
		Name:            "test-output-policy",
		Parallel:        true,
		ContinueOnError: true,
		Tasks:           []Task{pass2, fail2},
	}
	_ = parGroup.Run(t.Context(), parScope)
	parOut := parBuf.String()

	if strings.Contains(parOut, "passing task text") {
		t.Errorf("expected passing task text to be absent in parallel at default verbosity, got:\n%s", parOut)
	}
	if !strings.Contains(parOut, "failing task text") {
		t.Errorf("expected failing task text to be present in parallel, got:\n%s", parOut)
	}

	seqLines := normalizeLines(seqOut)
	parLines := normalizeLines(parOut)
	if strings.Join(seqLines, "\n") != strings.Join(parLines, "\n") {
		t.Errorf("expected parallel group to yield same set of lines as sequential group:\nsequential lines:\n%v\nparallel lines:\n%v", seqLines, parLines)
	}

	// At -v=1, passing task text is also printed
	t.Run("VerboseOnePrintsPassingOutput", func(t *testing.T) {
		fs := flag.NewFlagSet("klog", flag.ContinueOnError)
		klog.InitFlags(fs)
		_ = fs.Set("v", "1")
		defer fs.Set("v", "0")

		var vBuf syncBuffer
		vScope := newScope(t, &vBuf)
		pass3, fail3 := makeTasks()
		vGroup := &Group{
			Name:            "test-output-policy",
			ContinueOnError: true,
			Tasks:           []Task{pass3, fail3},
		}
		_ = vGroup.Run(t.Context(), vScope)
		vOut := vBuf.String()
		if !strings.Contains(vOut, "passing task text") {
			t.Errorf("expected passing task text at -v=1, got:\n%s", vOut)
		}
	})
}

func TestTaskLog_PerTaskBuffer(t *testing.T) {
	var taskABuf, taskBBuf string
	taskA := &FuncTask{
		Name: "task-a",
		RunFn: func(ctx context.Context, tc *TaskContext) error {
			tc.Log().Info("hello from task A")
			if b, ok := tc.Stderr().(interface{ String() string }); ok {
				taskABuf = b.String()
			}
			return nil
		},
	}
	taskB := &FuncTask{
		Name: "task-b",
		RunFn: func(ctx context.Context, tc *TaskContext) error {
			tc.Log().Info("hello from task B")
			// Also verify scoped.Log(ctx) writes to the same task buffer
			scoped.Log(ctx).Info("context message from task B")
			if b, ok := tc.Stderr().(interface{ String() string }); ok {
				taskBBuf = b.String()
			}
			return nil
		},
	}

	var outBuf syncBuffer
	scope := newTestScope(t).WithOutput(&outBuf, &outBuf)
	grp := &Group{
		Name:     "log-isolation-test",
		Tasks:    []Task{taskA, taskB},
		Parallel: true,
	}

	if err := grp.Run(t.Context(), scope); err != nil {
		t.Fatalf("grp.Run() failed: %v", err)
	}

	if !strings.Contains(taskABuf, "hello from task A") {
		t.Errorf("expected task A buffer to contain 'hello from task A', got:\n%s", taskABuf)
	}
	if strings.Contains(taskABuf, "hello from task B") {
		t.Errorf("expected task A buffer to NOT contain 'hello from task B', got:\n%s", taskABuf)
	}

	if !strings.Contains(taskBBuf, "hello from task B") {
		t.Errorf("expected task B buffer to contain 'hello from task B', got:\n%s", taskBBuf)
	}
	if !strings.Contains(taskBBuf, "context message from task B") {
		t.Errorf("expected task B buffer to contain 'context message from task B', got:\n%s", taskBBuf)
	}
	if strings.Contains(taskBBuf, "hello from task A") {
		t.Errorf("expected task B buffer to NOT contain 'hello from task A', got:\n%s", taskBBuf)
	}
}
