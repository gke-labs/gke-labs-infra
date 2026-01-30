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

	"github.com/gke-labs/gke-labs-infra/codestyle/pkg/walker"
	"k8s.io/klog/v2"
)

// testEvent represents a single event in a go test -json stream.
type testEvent struct {
	Time       time.Time
	Action     string
	Package    string
	ImportPath string
	Test       string
	Elapsed    float64
	Output     string
}

// Test runs go tests in discovered modules.
func Test(ctx context.Context, root string) error {
	// Find all go.mod files
	ignoreList := walker.NewIgnoreList([]string{".git", "vendor", "node_modules"})
	goMods, err := walker.Walk(root, ignoreList, func(path string, info os.FileInfo) bool {
		return info.Name() == "go.mod"
	})
	if err != nil {
		return err
	}

	buildDir := filepath.Join(root, ".build", "test-results")
	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return fmt.Errorf("failed to create build dir: %w", err)
	}

	for _, goMod := range goMods {
		dir := filepath.Dir(goMod)
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			return err
		}

		name := rel
		if name == "." {
			name = "root"
		}
		resultFile := filepath.Join(buildDir, name+".json")
		if err := os.MkdirAll(filepath.Dir(resultFile), 0755); err != nil {
			return err
		}

		klog.Infof("Running go vet in %s", dir)
		vetCmd := exec.CommandContext(ctx, "go", "vet", "./...")
		vetCmd.Dir = dir
		vetCmd.Stdout = os.Stdout
		vetCmd.Stderr = os.Stderr
		if err := vetCmd.Run(); err != nil {
			return fmt.Errorf("go vet failed in %s: %w", dir, err)
		}

		klog.Infof("Running go test in %s", dir)
		if err := runGoTest(ctx, dir, resultFile); err != nil {
			return fmt.Errorf("go test failed in %s: %w", dir, err)
		}
	}
	return nil
}

func runGoTest(ctx context.Context, dir string, resultFile string) error {
	f, err := os.Create(resultFile)
	if err != nil {
		return fmt.Errorf("failed to create result file: %w", err)
	}
	defer f.Close()

	cmd := exec.CommandContext(ctx, "go", "test", "-json", "./...")
	cmd.Dir = dir

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
			// If it's not JSON, we can't do much with it for pretty printing,
			// but it's already being written to the file via TeeReader.
			break
		}

		switch event.Action {
		case "pass":
			if event.Test != "" {
				fmt.Printf("PASS: %s\n", event.Test)
			}
		case "fail":
			if event.Test != "" {
				fmt.Printf("FAIL: %s\n", event.Test)
			}
		case "skip":
			if event.Test != "" {
				fmt.Printf("SKIP: %s\n", event.Test)
			}
		case "output":
			if event.Test == "" {
				// Only print package-level output if it's not the standard PASS/ok/FAIL summary
				// which is redundant with our PASS: TestFoo output.
				out := event.Output
				if out == "PASS\n" || out == "FAIL\n" ||
					strings.HasPrefix(out, "ok  \t") ||
					strings.HasPrefix(out, "FAIL\t") {
					continue
				}
				fmt.Print(out)
			}
		case "build-output":
			fmt.Print(event.Output)
		}
	}

	if err := cmd.Wait(); err != nil {
		return err
	}

	return nil
}
