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

func setupRaceTestRepo(t *testing.T, apYAML string) string {
	t.Helper()
	tempDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	apDir := filepath.Join(tempDir, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	if apYAML == "" {
		apYAML = "version: latest\n"
	}
	if err := os.WriteFile(filepath.Join(apDir, "ap.yaml"), []byte(apYAML), 0644); err != nil {
		t.Fatal(err)
	}

	goModContent := "module example.com/racetest\n\ngo 1.27\n"
	if err := os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte(goModContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Deliberate data race: two goroutines write to the same variable without synchronization.
	raceTestCode := `package racetest

import (
	"sync"
	"testing"
)

func TestDataRace(t *testing.T) {
	var count int
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		count = 1
	}()
	go func() {
		defer wg.Done()
		count = 2
	}()
	wg.Wait()
	_ = count
}
`
	if err := os.WriteFile(filepath.Join(tempDir, "race_test.go"), []byte(raceTestCode), 0644); err != nil {
		t.Fatal(err)
	}

	return tempDir
}

func TestRunTest_RaceDetector_Flag(t *testing.T) {
	tempDir := setupRaceTestRepo(t, "")

	// 1. Without race detector, test with data race passes
	optNoRace := TestOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			Dir:      tempDir,
		},
		Race: false,
	}
	if err := RunTest(t.Context(), optNoRace); err != nil {
		t.Fatalf("expected test without -race to pass, got: %v", err)
	}

	// 2. With race detector flag enabled, test fails due to data race
	optRace := TestOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			Dir:      tempDir,
		},
		Race: true,
	}
	if err := RunTest(t.Context(), optRace); err == nil {
		t.Fatal("expected test with -race to fail due to data race, but it passed")
	}
}

func TestRunTest_RaceDetector_Config(t *testing.T) {
	apYAML := `version: latest
test:
  race: true
`
	tempDir := setupRaceTestRepo(t, apYAML)

	// In a repo configured with test.race: true, RunTest defaults to running with -race
	opt := TestOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			Dir:      tempDir,
		},
	}
	if err := RunTest(t.Context(), opt); err == nil {
		t.Fatal("expected test to fail under race detector configured in ap.yaml, but it passed")
	}
}

func TestRunTest_RaceDetector_FlagOverridesConfig(t *testing.T) {
	apYAML := `version: latest
test:
  race: true
`
	tempDir := setupRaceTestRepo(t, apYAML)

	// Explicit --race=false overrides ap.yaml configuration
	opt := TestOptions{
		RootOptions: &RootOptions{
			RepoRoot: tempDir,
			Dir:      tempDir,
		},
		Race:         false,
		raceExplicit: true,
	}
	if err := RunTest(t.Context(), opt); err != nil {
		t.Fatalf("expected explicit --race=false to bypass race detector and pass, got: %v", err)
	}
}

func TestBuildTestCommand_RaceFlag(t *testing.T) {
	rootOpt := &RootOptions{}
	cmd := BuildTestCommand(rootOpt)

	flag := cmd.Flags().Lookup("race")
	if flag == nil {
		t.Fatal("expected --race flag to exist on test command")
	}
	if flag.DefValue != "false" {
		t.Errorf("expected --race default value to be false, got: %s", flag.DefValue)
	}
}
