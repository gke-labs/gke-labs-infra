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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildInitCommand(t *testing.T) {
	rootOpt := &RootOptions{}
	cmd := BuildInitCommand(rootOpt)
	if cmd == nil {
		t.Fatal("BuildInitCommand returned nil")
	}
	if cmd.Use != "init [path]" {
		t.Errorf("Unexpected use string: %q", cmd.Use)
	}
	if cmd.Flags().Lookup("license") == nil {
		t.Error("expected --license flag")
	}
	if cmd.Flags().Lookup("copyright-holder") == nil {
		t.Error("expected --copyright-holder flag")
	}
	if cmd.Flags().Lookup("generate") == nil {
		t.Error("expected --generate flag")
	}
	if cmd.Flags().Lookup("force") == nil {
		t.Error("expected --force flag")
	}
}

func TestRunInit_DryRun(t *testing.T) {
	tempDir := t.TempDir()
	targetDir := filepath.Join(tempDir, "testrepo")

	opt := InitOptions{
		RootOptions: &RootOptions{
			DryRun: true,
		},
		TargetDir:       targetDir,
		CopyrightHolder: "Test Org",
	}

	ctx := t.Context()
	if err := RunInit(ctx, opt); err != nil {
		t.Fatalf("RunInit dry-run failed: %v", err)
	}

	// Verify nothing was created
	if _, err := os.Stat(filepath.Join(targetDir, ".ap")); !os.IsNotExist(err) {
		t.Errorf("expected .ap not to exist in dry-run mode")
	}
}

func TestRunInit_Success(t *testing.T) {
	tempDir := t.TempDir()
	targetDir := filepath.Join(tempDir, "testrepo")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}

	opt := InitOptions{
		RootOptions:     &RootOptions{},
		TargetDir:       targetDir,
		License:         "apache-2.0",
		CopyrightHolder: "Test Org",
	}

	ctx := t.Context()
	if err := RunInit(ctx, opt); err != nil {
		t.Fatalf("RunInit failed: %v", err)
	}

	// Verify .ap/ap.yaml
	apYAML, err := os.ReadFile(filepath.Join(targetDir, ".ap", "ap.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(apYAML) != "version: latest\n" {
		t.Errorf("unexpected ap.yaml: %q", string(apYAML))
	}

	// Verify .ap/go.yaml
	goYAML, err := os.ReadFile(filepath.Join(targetDir, ".ap", "go.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goYAML), "gofmt:") {
		t.Errorf("expected gofmt in go.yaml: %q", string(goYAML))
	}

	// Verify .ap/headers.yaml
	headersYAML, err := os.ReadFile(filepath.Join(targetDir, ".ap", "headers.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(headersYAML), "copyrightHolder: Test Org") {
		t.Errorf("expected copyrightHolder in headers.yaml: %q", string(headersYAML))
	}
}

func TestRunInit_LicenseNone(t *testing.T) {
	tempDir := t.TempDir()
	targetDir := filepath.Join(tempDir, "testrepo")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}

	opt := InitOptions{
		RootOptions: &RootOptions{},
		TargetDir:   targetDir,
		License:     "none",
	}

	ctx := t.Context()
	if err := RunInit(ctx, opt); err != nil {
		t.Fatalf("RunInit failed: %v", err)
	}

	headersYAML, err := os.ReadFile(filepath.Join(targetDir, ".ap", "headers.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(headersYAML) != "license: none\n" {
		t.Errorf("unexpected headers.yaml: %q", string(headersYAML))
	}
}

func TestRunInit_WithGenerate(t *testing.T) {
	tempDir := t.TempDir()
	targetDir := filepath.Join(tempDir, "testrepo")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Initialize git repo so git rev-parse works
	cmd := exec.Command("git", "init")
	cmd.Dir = targetDir
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_CONFIG_") {
			env = append(env, e)
		}
	}
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init failed: %v", err)
	}

	opt := InitOptions{
		RootOptions: &RootOptions{
			RepoRoot: targetDir,
		},
		TargetDir:       targetDir,
		License:         "apache-2.0",
		CopyrightHolder: "Test Org",
		Generate:        true,
	}

	ctx := t.Context()
	if err := RunInit(ctx, opt); err != nil {
		t.Fatalf("RunInit with generate failed: %v", err)
	}

	// Verify generated CI scripts exist
	verifyScript := filepath.Join(targetDir, "dev", "ci", "presubmits", "ap-verify-generate")
	if _, err := os.Stat(verifyScript); err != nil {
		t.Errorf("expected %s to exist: %v", verifyScript, err)
	}

	// Verify generated workflow exists
	workflow := filepath.Join(targetDir, ".github", "workflows", "ci-presubmits.yaml")
	if _, err := os.Stat(workflow); err != nil {
		t.Errorf("expected %s to exist: %v", workflow, err)
	}
}

func TestBuildInitCommand_HiddenFlag(t *testing.T) {
	cmd := BuildInitCommand(&RootOptions{})
	f := cmd.Flags().Lookup("copyright")
	if f == nil {
		t.Fatal("expected copyright flag to exist")
	}
	if !f.Hidden {
		t.Errorf("expected copyright flag to be hidden")
	}
}

func TestDetectGitUserName(t *testing.T) {
	tempDir := t.TempDir()

	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_CONFIG_") {
			env = append(env, e)
		}
	}

	// Initialize git repo and set local user.name
	cmd := exec.Command("git", "init")
	cmd.Dir = tempDir
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	cfgCmd := exec.Command("git", "config", "user.name", "Test Specific User")
	cfgCmd.Dir = tempDir
	cfgCmd.Env = env
	if err := cfgCmd.Run(); err != nil {
		t.Fatal(err)
	}
	if name := detectGitUserName(t.Context(), tempDir); name != "Test Specific User" {
		t.Errorf("expected 'Test Specific User', got %q", name)
	}
}
