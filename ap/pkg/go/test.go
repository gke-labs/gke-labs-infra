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
	Time       time.Time `json:"Time"`
	Action     string    `json:"Action"`
	Package    string    `json:"Package"`
	ImportPath string    `json:"ImportPath"`
	Test       string    `json:"Test"`
	Elapsed    float64   `json:"Elapsed"`
	Output     string    `json:"Output"`
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

	buildDir := filepath.Join(root, ".build", "test-results", "go")
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
