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
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	if apYAML != "" {
		if err := os.WriteFile(filepath.Join(dir, ".ap", "ap.yaml"), []byte(apYAML), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/race\n\ngo 1.27\n"), 0644); err != nil {
		t.Fatal(err)
	}
	code := `package race

import (
	"sync"
	"testing"
)

func TestRace(t *testing.T) {
	var n int
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); n = 1 }()
	go func() { defer wg.Done(); n = 2 }()
	wg.Wait()
	_ = n
}
`
	if err := os.WriteFile(filepath.Join(dir, "race_test.go"), []byte(code), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunTest_Race(t *testing.T) {
	tempDir := setupRaceTestRepo(t, "")
	opt := TestOptions{
		RootOptions: &RootOptions{RepoRoot: tempDir, Dir: tempDir},
		Race:        true,
	}
	if err := RunTest(t.Context(), opt); err == nil {
		t.Fatal("expected test with race detector to fail, but it passed")
	}
}

func TestRunTest_ConfigRace(t *testing.T) {
	tempDir := setupRaceTestRepo(t, "version: latest\ntest:\n  race: true\n")
	cmd := BuildTestCommand(&RootOptions{RepoRoot: tempDir, Dir: tempDir})
	if err := cmd.ExecuteContext(t.Context()); err == nil {
		t.Fatal("expected test to fail under test.race in ap.yaml, but it passed")
	}
}

func TestRunTest_FlagOverridesConfig(t *testing.T) {
	tempDir := setupRaceTestRepo(t, "version: latest\ntest:\n  race: true\n")
	cmd := BuildTestCommand(&RootOptions{RepoRoot: tempDir, Dir: tempDir})
	cmd.SetArgs([]string{"--test.race=false"})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("expected --test.race=false to override config and pass, got: %v", err)
	}
}
