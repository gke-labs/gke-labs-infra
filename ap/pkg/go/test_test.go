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
	"os"
	"path/filepath"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

func TestHasGoTests(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Non-existent directory should return false, nil
	r1, err := sourcetree.Open(t.Context(), tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	hasTests, err := HasGoTests(r1, filepath.Join(tmpDir, "nonexistent"))
	if err != nil {
		t.Fatalf("expected nil error for nonexistent dir, got: %v", err)
	}
	if hasTests {
		t.Errorf("expected false for nonexistent dir, got true")
	}

	// 2. Empty directory
	emptyDir := filepath.Join(tmpDir, "empty")
	if err := os.Mkdir(emptyDir, 0755); err != nil {
		t.Fatal(err)
	}
	r2, err := sourcetree.Open(t.Context(), emptyDir)
	if err != nil {
		t.Fatal(err)
	}
	hasTests, err = HasGoTests(r2, emptyDir)
	if err != nil {
		t.Fatalf("unexpected error for empty dir: %v", err)
	}
	if hasTests {
		t.Errorf("expected false for empty dir, got true")
	}

	// 3. Directory with only non-test go files
	noTestDir := filepath.Join(tmpDir, "notests")
	if err := os.Mkdir(noTestDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noTestDir, "foo.go"), []byte("package foo"), 0644); err != nil {
		t.Fatal(err)
	}
	r3, err := sourcetree.Open(t.Context(), noTestDir)
	if err != nil {
		t.Fatal(err)
	}
	hasTests, err = HasGoTests(r3, noTestDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasTests {
		t.Errorf("expected false for dir without test files, got true")
	}

	// 4. Directory with a _test.go file
	withTestDir := filepath.Join(tmpDir, "withtests")
	if err := os.Mkdir(withTestDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(withTestDir, "foo_test.go"), []byte("package foo"), 0644); err != nil {
		t.Fatal(err)
	}
	r4, err := sourcetree.Open(t.Context(), withTestDir)
	if err != nil {
		t.Fatal(err)
	}
	hasTests, err = HasGoTests(r4, withTestDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasTests {
		t.Errorf("expected true for dir with foo_test.go, got false")
	}

	// 5. Directory with _test.go file in a nested subdirectory
	nestedDir := filepath.Join(tmpDir, "nested")
	subNested := filepath.Join(nestedDir, "sub", "sub2")
	if err := os.MkdirAll(subNested, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subNested, "bar_test.go"), []byte("package bar"), 0644); err != nil {
		t.Fatal(err)
	}
	r5, err := sourcetree.Open(t.Context(), nestedDir)
	if err != nil {
		t.Fatal(err)
	}
	hasTests, err = HasGoTests(r5, nestedDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasTests {
		t.Errorf("expected true for nested test files, got false")
	}
}

func TestRace(t *testing.T) {
	tmpDir := t.TempDir()
	tree, err := sourcetree.Open(t.Context(), tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	scope := tasks.NewTaskContext(tmpDir, tmpDir, tree)
	if scope.Race() {
		t.Errorf("expected scope.Race() to be false by default")
	}
	scope = scope.WithRace(true)
	if !scope.Race() {
		t.Errorf("expected scope.Race() to be true after WithRace(true)")
	}
	scope = scope.WithRace(false)
	if scope.Race() {
		t.Errorf("expected scope.Race() to be false after WithRace(false)")
	}
}
