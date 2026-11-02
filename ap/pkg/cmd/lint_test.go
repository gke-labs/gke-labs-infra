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

package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/baseline"
)

func TestRunLint_UnknownKeyFails(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmpDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	apDir := filepath.Join(tmpDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module example.com/test\n\ngo 1.27\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Misspelled key under lint.unused
	yamlContent := `
lint:
  unused:
    skipGenerted: false
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(tmpDir)

	opt := LintOptions{
		RootOptions: &RootOptions{
			RepoRoot: tmpDir,
			APRoots:  []string{tmpDir},
			DryRun:   true,
		},
	}
	err := RunLint(t.Context(), opt)
	if err == nil {
		t.Fatalf("expected RunLint to fail with unknown key error")
	}
	if !strings.Contains(err.Error(), `"lint.unused.skipGenerted"`) {
		t.Errorf("expected error to name key path \"lint.unused.skipGenerted\", got: %v", err)
	}
	if !strings.Contains(err.Error(), `"skipGenerated"`) {
		t.Errorf("expected error to suggest \"skipGenerated\", got: %v", err)
	}
}

func TestRunLint_ClosewriterBaseline(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmpDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	apDir := filepath.Join(tmpDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module example.com/test\n\ngo 1.27\n"), 0644); err != nil {
		t.Fatal(err)
	}

	codeWithFinding := `package main

import (
	"log"
	"os"
)

func writeData(dst string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("failed: %v", err)
		}
	}()
	return nil
}

func main() {
	if err := writeData("test.txt"); err != nil {
		log.Fatal(err)
	}
}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(codeWithFinding), 0644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(tmpDir)

	opt := LintOptions{
		RootOptions: &RootOptions{
			RepoRoot: tmpDir,
			APRoots:  []string{tmpDir},
		},
	}

	// 1. ap lint fails with closewriter error
	err := RunLint(t.Context(), opt)
	if err == nil {
		t.Fatalf("expected RunLint to fail on closewriter finding")
	}
	if !strings.Contains(err.Error(), "closewriter check failed") {
		t.Fatalf("expected closewriter check failure, got: %v", err)
	}

	// 2. ap lint --write-baseline creates .ap/closewriter-baseline.txt with one line
	optWrite := LintOptions{
		RootOptions: &RootOptions{
			RepoRoot: tmpDir,
			APRoots:  []string{tmpDir},
		},
		WriteBaseline: true,
	}
	if err := RunLint(t.Context(), optWrite); err != nil {
		t.Fatalf("ap lint --write-baseline failed: %v", err)
	}

	baselineFile := filepath.Join(apDir, "closewriter-baseline.txt")
	entries, err := baseline.Load(baselineFile)
	if err != nil {
		t.Fatalf("failed to load baseline: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 baseline entry, got %d", len(entries))
	}
	if entries[0].File != "main.go" || entries[0].EnclosingFunc != "writeData" {
		t.Errorf("unexpected baseline entry: %+v", entries[0])
	}

	// 3. ap lint passes
	if err := RunLint(t.Context(), opt); err != nil {
		t.Fatalf("expected ap lint to pass with baseline, got: %v", err)
	}

	// 4. fixing the code reports the stale entry
	fixedCode := `package main

import (
	"log"
	"os"
)

func writeData(dst string) (retErr error) {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() {
		if err := f.Close(); err != nil && retErr == nil {
			retErr = err
		}
	}()
	return nil
}

func main() {
	if err := writeData("test.txt"); err != nil {
		log.Fatal(err)
	}
}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(fixedCode), 0644); err != nil {
		t.Fatal(err)
	}

	// Capture stdout to verify stale baseline entry report
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	lintErr := RunLint(t.Context(), opt)

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	output := buf.String()

	if lintErr != nil {
		t.Fatalf("expected ap lint to pass after fix, got: %v", lintErr)
	}
	if !strings.Contains(output, "stale baseline entry") {
		t.Errorf("expected output to report stale baseline entry, got:\n%s", output)
	}
}
