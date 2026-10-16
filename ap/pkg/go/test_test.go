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
	"reflect"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/repo"
)

func TestHasGoTests(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Non-existent directory should return false, nil
	r1, err := repo.Open(tmpDir)
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
	r2, err := repo.Open(emptyDir)
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
	r3, err := repo.Open(noTestDir)
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
	r4, err := repo.Open(withTestDir)
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
	r5, err := repo.Open(nestedDir)
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

func TestParseShardSelector(t *testing.T) {
	tests := []struct {
		input     string
		wantI     int
		wantN     int
		expectErr bool
	}{
		{"1/3", 1, 3, false},
		{" 2 / 3 ", 2, 3, false},
		{"3/3", 3, 3, false},
		{"1/1", 1, 1, false},
		{"0/3", 0, 0, true},
		{"4/3", 0, 0, true},
		{"1/0", 0, 0, true},
		{"-1/3", 0, 0, true},
		{"1/-3", 0, 0, true},
		{"foo", 0, 0, true},
		{"1/2/3", 0, 0, true},
		{"1/", 0, 0, true},
		{"/3", 0, 0, true},
		{"", 0, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			i, n, err := ParseShardSelector(tt.input)
			if tt.expectErr {
				if err == nil {
					t.Fatalf("ParseShardSelector(%q) expected error, got nil (%d, %d)", tt.input, i, n)
				}
			} else {
				if err != nil {
					t.Fatalf("ParseShardSelector(%q) unexpected error: %v", tt.input, err)
				}
				if i != tt.wantI || n != tt.wantN {
					t.Fatalf("ParseShardSelector(%q) = (%d, %d), want (%d, %d)", tt.input, i, n, tt.wantI, tt.wantN)
				}
			}
		})
	}
}

func TestSelectShardTests(t *testing.T) {
	all11 := []string{
		"TestK", "TestJ", "TestI", "TestH", "TestG",
		"TestF", "TestE", "TestD", "TestC", "TestB", "TestA",
	}

	// Shard across 3 workers
	s1 := SelectShardTests(all11, 1, 3)
	s2 := SelectShardTests(all11, 2, 3)
	s3 := SelectShardTests(all11, 3, 3)

	expectedS1 := []string{"TestA", "TestD", "TestG", "TestJ"}
	expectedS2 := []string{"TestB", "TestE", "TestH", "TestK"}
	expectedS3 := []string{"TestC", "TestF", "TestI"}

	if !reflect.DeepEqual(s1, expectedS1) {
		t.Errorf("s1: got %v, want %v", s1, expectedS1)
	}
	if !reflect.DeepEqual(s2, expectedS2) {
		t.Errorf("s2: got %v, want %v", s2, expectedS2)
	}
	if !reflect.DeepEqual(s3, expectedS3) {
		t.Errorf("s3: got %v, want %v", s3, expectedS3)
	}

	// Verify all 11 tests are accounted for without duplication
	union := append(append(s1, s2...), s3...)
	if len(union) != 11 {
		t.Fatalf("expected 11 tests in union, got %d", len(union))
	}

	// Test deduplication
	withDups := []string{"TestB", "TestA", "TestB", "TestC", "TestA"}
	d1 := SelectShardTests(withDups, 1, 2)
	d2 := SelectShardTests(withDups, 2, 2)
	if !reflect.DeepEqual(d1, []string{"TestA", "TestC"}) {
		t.Errorf("d1: got %v, want [TestA, TestC]", d1)
	}
	if !reflect.DeepEqual(d2, []string{"TestB"}) {
		t.Errorf("d2: got %v, want [TestB]", d2)
	}

	// Test shard index beyond test count
	single := []string{"TestOnly"}
	if res := SelectShardTests(single, 2, 3); len(res) != 0 {
		t.Errorf("expected empty for shard 2/3 with 1 test, got %v", res)
	}
}

func TestBuildRunRegex(t *testing.T) {
	if got := BuildRunRegex(nil); got != "^$" {
		t.Errorf("BuildRunRegex(nil) = %q, want %q", got, "^$")
	}
	if got := BuildRunRegex([]string{}); got != "^$" {
		t.Errorf("BuildRunRegex([]) = %q, want %q", got, "^$")
	}
	if got := BuildRunRegex([]string{"TestOne"}); got != "^(TestOne)$" {
		t.Errorf("BuildRunRegex([TestOne]) = %q, want %q", got, "^(TestOne)$")
	}
	if got := BuildRunRegex([]string{"TestOne", "TestTwo"}); got != "^(TestOne|TestTwo)$" {
		t.Errorf("BuildRunRegex([TestOne, TestTwo]) = %q, want %q", got, "^(TestOne|TestTwo)$")
	}
}

func TestListE2ETestsAndGoE2eTask(t *testing.T) {
	dir := t.TempDir()

	// Write go.mod
	goMod := "module example.com/testmod\n\ngo 1.24\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}

	e2eDir := filepath.Join(dir, "tests", "e2e")
	if err := os.MkdirAll(e2eDir, 0755); err != nil {
		t.Fatal(err)
	}

	testCode := `package e2e

import "testing"

func TestAlpha(t *testing.T) {}
func TestBeta(t *testing.T) {}
func TestGamma(t *testing.T) {}
`
	if err := os.WriteFile(filepath.Join(e2eDir, "e2e_test.go"), []byte(testCode), 0644); err != nil {
		t.Fatal(err)
	}

	tests, err := ListE2ETests(t.Context(), dir, "")
	if err != nil {
		t.Fatalf("ListE2ETests failed: %v", err)
	}
	expected := []string{"TestAlpha", "TestBeta", "TestGamma"}
	if !reflect.DeepEqual(tests, expected) {
		t.Fatalf("ListE2ETests = %v, want %v", tests, expected)
	}

	// Filter pattern
	filteredTests, err := ListE2ETests(t.Context(), dir, "TestB.*")
	if err != nil {
		t.Fatalf("ListE2ETests with pattern failed: %v", err)
	}
	if !reflect.DeepEqual(filteredTests, []string{"TestBeta"}) {
		t.Fatalf("ListE2ETests with pattern = %v, want [TestBeta]", filteredTests)
	}

	// Run GoE2eTask shard 1 of 2
	resultFile := filepath.Join(dir, "result.json")
	task := &GoE2eTask{
		Dir:        dir,
		Name:       "testmod",
		ResultFile: resultFile,
		Shard:      "1/2",
		Timeout:    "10s",
	}

	if err := task.Run(t.Context(), nil); err != nil {
		t.Fatalf("task.Run failed: %v", err)
	}

	if _, err := os.Stat(resultFile); err != nil {
		t.Fatalf("expected result file to be created: %v", err)
	}
}
