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

package golang

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

// testEvent represents a single event in a go test -json stream.
type testEvent struct {
	Time       time.Time `json:"Time"`
	Action     string    `json:"Action"`
	Package    string    `json:"Package"`
	ImportPath string    `json:"ImportPath"`
	Test       string    `json:"Test"`
	Elapsed    float64   `json:"Elapsed"`
	Output     string    `json:"Output"`
}

// GoTestTask represents a task to run go tests in a single module.
type GoTestTask struct {
	Dir        string
	Name       string
	ResultFile string
}

func (t *GoTestTask) Run(ctx context.Context, scope *tasks.TaskContext) error {
	scoped.Log(ctx).Info("running go test", "dir", t.Dir)
	opts := RunGoTestOptions{
		Stdout: scope.Stdout(),
		Stderr: scope.Stderr(),
		Race:   scope.Race(),
	}
	if err := RunGoTest(ctx, scope, t.Dir, t.ResultFile, opts); err != nil {
		return fmt.Errorf("go test failed in %s: %w", t.Dir, err)
	}
	return nil
}

func (t *GoTestTask) GetName() string {
	return fmt.Sprintf("go-test-%s", t.Name)
}

func (t *GoTestTask) GetChildren() []tasks.Task {
	return nil
}

func (t *GoTestTask) SourcePath() string {
	return t.Dir
}

var _ tasks.Sourced = (*GoTestTask)(nil)

// GoE2eTask represents a task to run go e2e tests.
type GoE2eTask struct {
	Dir        string
	Name       string
	ResultFile string
}

func (t *GoE2eTask) Run(ctx context.Context, scope *tasks.TaskContext) error {
	scoped.Log(ctx).Info("running go e2e test", "dir", t.Dir)
	opts := RunGoTestOptions{
		Env:    []string{"RUN_E2E=1"},
		Args:   []string{"-v", "-count=1", "-timeout", "30m", "./tests/e2e/..."},
		Stdout: scope.Stdout(),
		Stderr: scope.Stderr(),
	}
	if err := RunGoTest(ctx, scope, t.Dir, t.ResultFile, opts); err != nil {
		return fmt.Errorf("go e2e test failed in %s: %w", t.Dir, err)
	}
	return nil
}

func (t *GoE2eTask) GetName() string {
	return fmt.Sprintf("go-e2e-%s", t.Name)
}

func (t *GoE2eTask) GetChildren() []tasks.Task {
	return nil
}

func (t *GoE2eTask) SourcePath() string {
	return t.Dir
}

var _ tasks.Sourced = (*GoE2eTask)(nil)

// HasGoTests returns true if the directory or any subdirectory contains a .go file with a _test.go suffix.
func HasGoTests(r *sourcetree.SourceTree, dir string) (bool, error) {
	return r.Under(dir).HasGoTests(), nil
}

// TestTasks returns a task group for running go tests in discovered modules.
func TestTasks(r *sourcetree.SourceTree, root string) (tasks.Task, error) {
	modules := r.Under(root).Modules()

	buildDir := filepath.Join(root, ".build", "test-results", "go")
	if artifactsDir := os.Getenv("ARTIFACTS"); artifactsDir != "" {
		buildDir = filepath.Join(artifactsDir, "test-results", "go")
	}
	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create build dir: %w", err)
	}

	var testTasks []tasks.Task
	for _, dir := range modules {
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			return nil, err
		}

		name := rel
		if name == "." {
			name = "root"
		}
		resultFile := filepath.Join(buildDir, name+".json")
		if err := os.MkdirAll(filepath.Dir(resultFile), 0755); err != nil {
			return nil, err
		}

		testTasks = append(testTasks, &GoTestTask{
			Dir:        dir,
			Name:       name,
			ResultFile: resultFile,
		})
	}

	return &tasks.Group{
		Name:            "go-tests",
		Tasks:           testTasks,
		ContinueOnError: true,
	}, nil
}

// Test runs go tests in discovered modules.
func Test(ctx context.Context, r *sourcetree.SourceTree, root string) error {
	t, err := TestTasks(r, root)
	if err != nil {
		return err
	}
	return t.Run(ctx, tasks.NewTaskContext(root, root, r))
}

// RunGoTestOptions holds options for running go tests.
type RunGoTestOptions struct {
	Env    []string
	Args   []string
	Stdout io.Writer
	Stderr io.Writer
	Race   bool
}

// RunGoTest runs go tests in the given directory using the task scope.
func RunGoTest(ctx context.Context, scope *tasks.TaskContext, dir string, resultFile string, opts RunGoTestOptions) error {
	log := scoped.Log(ctx)

	args := []string{"test", "-json"}
	if opts.Race {
		args = append(args, "-race")
	}
	if len(opts.Args) > 0 {
		args = append(args, opts.Args...)
	} else {
		args = append(args, "./...")
	}

	result, commandErr := scope.RunCommand(ctx, tasks.Command{
		Program:               "go",
		Args:                  args,
		Dir:                   dir,
		Env:                   opts.Env,
		MachineReadableStdout: true,
	})

	if resultFile != "" {
		data, err := os.ReadFile(result.StdoutFile)
		if err != nil {
			return fmt.Errorf("reading test output: %w", err)
		}
		if err := os.WriteFile(resultFile, data, 0644); err != nil {
			return fmt.Errorf("writing test result file: %w", err)
		}
	}

	file, err := os.Open(result.StdoutFile)
	if err != nil {
		return commandErr
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			log.Fine("failed to close test output file", "error", closeErr)
		}
	}()

	var outputBuffer bytes.Buffer
	decoder := json.NewDecoder(file)

	for {
		var event testEvent
		if err := decoder.Decode(&event); err != nil {
			if err != io.EOF {
				scoped.Log(ctx).Warn("failed to decode test event", "error", err)
			}
			break
		}

		indent := strings.Repeat("    ", strings.Count(event.Test, "/"))

		switch event.Action {
		case "pass":
			if event.Test != "" {
				fmt.Fprintf(&outputBuffer, "%s--- PASS: %s (%.2fs)\n", indent, event.Test, event.Elapsed)
			}
		case "fail":
			if event.Test != "" {
				fmt.Fprintf(&outputBuffer, "%s--- FAIL: %s (%.2fs)\n", indent, event.Test, event.Elapsed)
			}
		case "skip":
			if event.Test != "" {
				fmt.Fprintf(&outputBuffer, "%s--- SKIP: %s (%.2fs)\n", indent, event.Test, event.Elapsed)
			}
		case "output":
			out := event.Output
			if event.Test == "" {
				// Only print package-level output if it's not the standard PASS/ok/FAIL summary
				// which is redundant with our PASS: TestFoo output.
				if out == "PASS\n" || out == "FAIL\n" ||
					strings.HasPrefix(out, "ok  \t") ||
					strings.HasPrefix(out, "FAIL\t") {
					continue
				}
			}
			outputBuffer.WriteString(out)
		case "build-output":
			outputBuffer.WriteString(event.Output)
		case "run", "pause", "cont", "bench", "start", "build-fail":
			// Ignore these for pretty printing
		default:
			scoped.Log(ctx).Warn("unknown test action", "action", event.Action)
		}
	}

	if outputBuffer.Len() > 0 && opts.Stdout != nil {
		if _, writeError := opts.Stdout.Write(outputBuffer.Bytes()); writeError != nil {
			log.Fine("failed to write test output", "error", writeError)
		}
	}

	return commandErr
}
