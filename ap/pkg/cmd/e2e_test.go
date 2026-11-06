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
)

func setupE2eRepo(t *testing.T) string {
	t.Helper()
	tempDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tempDir, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, ".ap", "ap.yaml"), []byte("version: 1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	devTasksDir := filepath.Join(tempDir, "dev", "tasks")
	if err := os.MkdirAll(devTasksDir, 0755); err != nil {
		t.Fatal(err)
	}

	scriptContent := `#!/bin/bash
echo "=== RUN   TestRunLifecycle"
echo "--- PASS: TestRunLifecycle (1.23s)"
echo "PASS"
`
	scriptPath := filepath.Join(devTasksDir, "test-e2e")
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatal(err)
	}
	return tempDir
}

func TestRunE2e_InlineOutput(t *testing.T) {
	tempDir := setupE2eRepo(t)
	opt := E2eOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			Dir:      tempDir,
		},
	}
	if err := opt.Resolve(nil); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	runErr := RunE2e(t.Context(), opt)

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	stdoutStr := buf.String()

	if runErr != nil {
		t.Fatalf("RunE2e failed: %v", runErr)
	}
	if !strings.Contains(stdoutStr, "=== test-e2e (ok,") {
		t.Errorf("expected stdout to contain '=== test-e2e (ok,', got:\n%s", stdoutStr)
	}
	if !strings.Contains(stdoutStr, "--- PASS: TestRunLifecycle") {
		t.Errorf("expected stdout to contain '--- PASS: TestRunLifecycle', got:\n%s", stdoutStr)
	}
}

func TestRunE2e_Artifacts(t *testing.T) {
	tempDir := setupE2eRepo(t)
	artifactsDir := filepath.Join(tempDir, "artifacts")
	t.Setenv("ARTIFACTS", artifactsDir)

	opt := E2eOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			Dir:      tempDir,
		},
	}
	if err := opt.Resolve(nil); err != nil {
		t.Fatalf("opt.Resolve failed: %v", err)
	}

	if err := RunE2e(t.Context(), opt); err != nil {
		t.Fatalf("RunE2e failed: %v", err)
	}

	logFile := filepath.Join(artifactsDir, "tasks", "test-e2e.log")
	logContent, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read artifact log %s: %v", logFile, err)
	}
	if !strings.Contains(string(logContent), "--- PASS: TestRunLifecycle") {
		t.Errorf("expected artifact log to contain '--- PASS: TestRunLifecycle', got:\n%s", string(logContent))
	}
}
