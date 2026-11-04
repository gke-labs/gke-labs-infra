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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"k8s.io/klog/v2"
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

func (t *GoTestTask) Run(ctx context.Context, scope *tasks.APScope) error {
	klog.Infof("Running go test in %s", t.Dir)
	if err := RunGoTest(ctx, t.Dir, t.ResultFile, RunGoTestOptions{}); err != nil {
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

// GoE2eTask represents a task to run go e2e tests.
type GoE2eTask struct {
	Dir        string
	Name       string
	ResultFile string
}

func (t *GoE2eTask) Run(ctx context.Context, scope *tasks.APScope) error {
	klog.Infof("Running go e2e test in %s", t.Dir)
	opts := RunGoTestOptions{
		Env:  []string{"RUN_E2E=1"},
		Args: []string{"-v", "-count=1", "-timeout", "30m", "./tests/e2e/..."},
	}
	if err := RunGoTest(ctx, t.Dir, t.ResultFile, opts); err != nil {
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
	return t.Run(ctx, tasks.NewScope(root, root, r))
}

// RunGoTestOptions holds options for running go tests.
type RunGoTestOptions struct {
	Env  []string
	Args []string
}

// RunGoTest runs go tests in the given directory.
func RunGoTest(ctx context.Context, dir string, resultFile string, opts RunGoTestOptions) (retErr error) {
	f, err := os.Create(resultFile)
	if err != nil {
		return fmt.Errorf("failed to create result file: %w", err)
	}
	defer func() {
		if err := f.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("failed to close result file: %w", err)
		}
	}()

	args := []string{"test", "-json"}
	if len(opts.Args) > 0 {
		args = append(args, opts.Args...)
	} else {
		args = append(args, "./...")
	}

	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), opts.Env...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return err
	}

	// Read from stdout, write to file AND process for pretty print
	tr := io.TeeReader(stdout, f)
	decoder := json.NewDecoder(tr)

	for {
		var event testEvent
		if err := decoder.Decode(&event); err != nil {
			if err == io.EOF {
				break
			}
			klog.Warningf("failed to decode test event: %v", err)
			break
		}

		indent := strings.Repeat("    ", strings.Count(event.Test, "/"))

		switch event.Action {
		case "pass":
			if event.Test != "" {
				fmt.Printf("%s--- PASS: %s (%.2fs)\n", indent, event.Test, event.Elapsed)
			}
		case "fail":
			if event.Test != "" {
				fmt.Printf("%s--- FAIL: %s (%.2fs)\n", indent, event.Test, event.Elapsed)
			}
		case "skip":
			if event.Test != "" {
				fmt.Printf("%s--- SKIP: %s (%.2fs)\n", indent, event.Test, event.Elapsed)
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
			fmt.Print(out)
		case "build-output":
			fmt.Print(event.Output)
		case "run", "pause", "cont", "bench", "start", "build-fail":
			// Ignore these for pretty printing
		default:
			klog.Warningf("unknown test action: %s", event.Action)
		}
	}

	if err := cmd.Wait(); err != nil {
		return err
	}

	return nil
}
