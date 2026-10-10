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
	"path/filepath"
	"testing"
)

func TestBuildDroppedErrorsCommand(t *testing.T) {
	cmd := BuildDroppedErrorsCommand(&RootOptions{})
	if cmd.Use != "droppederrors [packages...]" {
		t.Errorf("unexpected Use: %s", cmd.Use)
	}
	if cmd.Flags().Lookup("mode") == nil {
		t.Errorf("missing --mode flag")
	}
	if cmd.Flags().Lookup("baseline") == nil {
		t.Errorf("missing --baseline flag")
	}
	if cmd.Flags().Lookup("write-baseline") == nil {
		t.Errorf("missing --write-baseline flag")
	}
	if cmd.Flags().Lookup("exclude") == nil {
		t.Errorf("missing --exclude flag")
	}
	if cmd.Flags().Lookup("skip-tests") == nil {
		t.Errorf("missing --skip-tests flag")
	}
	if cmd.Flags().Lookup("skip-generated") == nil {
		t.Errorf("missing --skip-generated flag")
	}
	if cmd.Flags().Lookup("use-default-excludes") == nil {
		t.Errorf("missing --use-default-excludes flag")
	}
	if cmd.Flags().Lookup("goos") == nil {
		t.Errorf("missing --goos flag")
	}
}

func TestRunDroppedErrors_EndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmpDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(tmpDir, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	goMod := "module example.com/cmdtest\n\ngo 1.27\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}
	mainGo := `package main

import "errors"

func fail() error { return errors.New("fail") }

func main() {
	_ = fail()
}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(mainGo), 0644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(tmpDir)

	baselineFile := filepath.Join(tmpDir, ".ap", "droppederrors-baseline.txt")

	// 1. Write baseline via RunDroppedErrors
	optWrite := DroppedErrorsOptions{
		RootOptions: &RootOptions{
			RepoRoot: tmpDir,
			APRoot:   tmpDir,
		},
		Baseline:      baselineFile,
		WriteBaseline: true,
		SkipTests:     true,
	}
	if err := RunDroppedErrors(t.Context(), nil, optWrite, nil); err != nil {
		t.Fatalf("RunDroppedErrors with write-baseline failed: %v", err)
	}

	if _, err := os.Stat(baselineFile); err != nil {
		t.Fatalf("expected baseline file to exist: %v", err)
	}

	// 2. Run with mode error and baseline -> should pass
	optCheck := DroppedErrorsOptions{
		RootOptions: &RootOptions{
			RepoRoot: tmpDir,
			APRoot:   tmpDir,
		},
		Baseline:  baselineFile,
		Mode:      "error",
		SkipTests: true,
	}
	if err := RunDroppedErrors(t.Context(), nil, optCheck, nil); err != nil {
		t.Fatalf("RunDroppedErrors in mode error with baseline failed: %v", err)
	}

	// 3. Add new dropped error -> mode error should fail
	newMainGo := `package main

import "errors"

func fail() error { return errors.New("fail") }
func fail2() error { return errors.New("fail2") }

func main() {
	_ = fail()
	_ = fail2()
}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(newMainGo), 0644); err != nil {
		t.Fatal(err)
	}

	if err := RunDroppedErrors(t.Context(), nil, optCheck, nil); err == nil {
		t.Fatalf("expected RunDroppedErrors to fail on new dropped error in mode error")
	}

	// 4. Mode warn should not fail even with new dropped error
	optWarn := DroppedErrorsOptions{
		RootOptions: &RootOptions{
			RepoRoot: tmpDir,
			APRoot:   tmpDir,
		},
		Baseline:  baselineFile,
		Mode:      "warn",
		SkipTests: true,
	}
	if err := RunDroppedErrors(t.Context(), nil, optWarn, nil); err != nil {
		t.Fatalf("expected mode warn to not fail: %v", err)
	}
}

func TestRunDroppedErrors_InvalidMode(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmpDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	goMod := "module example.com/modetest\n\ngo 1.27\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tmpDir)

	opt := DroppedErrorsOptions{
		RootOptions: &RootOptions{
			RepoRoot: tmpDir,
			APRoot:   tmpDir,
		},
		Mode: "eror",
	}
	if err := RunDroppedErrors(t.Context(), nil, opt, nil); err == nil {
		t.Fatalf("expected RunDroppedErrors to fail on invalid mode 'eror'")
	}
}

func TestRunDroppedErrors_OutsideGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	// No .git directory created!
	goMod := "module example.com/baremod\n\ngo 1.27\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tmpDir)

	opt := DroppedErrorsOptions{
		Mode: "error",
	}
	// Should NOT fail with "could not find git repository root"
	if err := RunDroppedErrors(t.Context(), nil, opt, nil); err != nil {
		t.Fatalf("expected RunDroppedErrors to succeed outside git repo, got: %v", err)
	}
}

func TestRunDroppedErrors_ConfigSkipTestsFalse(t *testing.T) {
	tmpDir := t.TempDir()
	goMod := "module example.com/skiptest\n\ngo 1.27\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}
	mainGo := `package main

func main() {}
`
	testGo := `package main

import (
	"errors"
	"testing"
)

func fail() error { return errors.New("err") }

func TestFoo(t *testing.T) {
	_ = fail()
}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(mainGo), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "main_test.go"), []byte(testGo), 0644); err != nil {
		t.Fatal(err)
	}

	apDir := filepath.Join(tmpDir, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	goYaml := `lint:
  droppedErrors:
    mode: error
    skipTests: false
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(goYaml), 0644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(tmpDir)

	cmd := BuildDroppedErrorsCommand(&RootOptions{})
	// Run command with no flags explicitly set for skip-tests
	// It should read skipTests: false from .ap/go.yaml and fail on main_test.go
	opt := DroppedErrorsOptions{
		RootOptions: &RootOptions{},
		SkipTests:   true, // default value when flag not provided
	}
	err := RunDroppedErrors(t.Context(), cmd, opt, nil)
	if err == nil {
		t.Fatalf("expected RunDroppedErrors to fail because skipTests: false in config should check main_test.go")
	}
}

func TestRunDroppedErrors_ConfigSkipGenerated(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmpDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	apDir := filepath.Join(tmpDir, ".ap")
	if err := os.Mkdir(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	goMod := "module example.com/cmdtest\n\ngo 1.27\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}
	genGo := `// Code generated by test. DO NOT EDIT.
package main

import "errors"

func fail() error { return errors.New("fail") }

func main() {
	_ = fail()
}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "gen.go"), []byte(genGo), 0644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(tmpDir)

	// 1. lint.skipGenerated: true -> should skip gen.go and succeed
	goYaml := `lint:
  skipGenerated: true
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(goYaml), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := BuildDroppedErrorsCommand(&RootOptions{})
	opt := DroppedErrorsOptions{
		RootOptions:   &RootOptions{},
		SkipGenerated: false,
		SkipTests:     true,
	}
	if err := RunDroppedErrors(t.Context(), cmd, opt, nil); err != nil {
		t.Fatalf("expected RunDroppedErrors to succeed with lint.skipGenerated: true, got: %v", err)
	}

	// 2. default (no skipGenerated) -> should flag gen.go and fail
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	cmdDefault := BuildDroppedErrorsCommand(&RootOptions{})
	optDefault := DroppedErrorsOptions{
		RootOptions:   &RootOptions{},
		SkipGenerated: false,
		SkipTests:     true,
	}
	if err := RunDroppedErrors(t.Context(), cmdDefault, optDefault, nil); err == nil {
		t.Fatalf("expected RunDroppedErrors to fail by default on generated file")
	}

	// 3. lint.skipGenerated: true, but droppedErrors.skipGenerated: false override -> should flag gen.go
	overrideYaml := `lint:
  skipGenerated: true
  droppedErrors:
    skipGenerated: false
`
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(overrideYaml), 0644); err != nil {
		t.Fatal(err)
	}
	cmdOverride := BuildDroppedErrorsCommand(&RootOptions{})
	optOverride := DroppedErrorsOptions{
		RootOptions:   &RootOptions{},
		SkipGenerated: false,
		SkipTests:     true,
	}
	if err := RunDroppedErrors(t.Context(), cmdOverride, optOverride, nil); err == nil {
		t.Fatalf("expected RunDroppedErrors to fail when droppedErrors.skipGenerated: false overrides lint.skipGenerated: true")
	}
}
