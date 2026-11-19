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

package baseline

import (
	"bytes"
	"context"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
)

func TestParse(t *testing.T) {
	input := `# Some comment
pkg/foo/bar.go:MyFunc:(*os.File).Close

# Another comment with spaces
pkg/baz/qux.go  (*Server).Serve   fmt.Fprintln
`
	entries, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].File != "pkg/foo/bar.go" || entries[0].EnclosingFunc != "MyFunc" || entries[0].Detail != "(*os.File).Close" {
		t.Errorf("entry 0 mismatch: %+v", entries[0])
	}
	if entries[1].File != "pkg/baz/qux.go" || entries[1].EnclosingFunc != "(*Server).Serve" || entries[1].Detail != "fmt.Fprintln" {
		t.Errorf("entry 1 mismatch: %+v", entries[1])
	}
}

func TestParse_Category(t *testing.T) {
	input := `
pkg/foo/bar.go:MyFunc:error-only-checked-for-success:myFunc
pkg/foo/bar.go:MyFunc:error-discarded:closeErr
pkg/baz/qux.go:MyFunc:myOtherFunc
`
	entries, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].Category != CategoryErrorOnlyCheckedForSuccess || entries[0].Detail != "myFunc" {
		t.Errorf("entry 0 mismatch: %+v", entries[0])
	}
	if entries[1].Category != CategoryErrorDiscarded || entries[1].Detail != "closeErr" {
		t.Errorf("entry 1 mismatch: %+v", entries[1])
	}
	if entries[2].Category != "" || entries[2].Detail != "myOtherFunc" {
		t.Errorf("entry 2 mismatch: %+v", entries[2])
	}
}

func TestFormat(t *testing.T) {
	tests := []struct {
		analyzer    string
		wantHeader  string
		wantEntries string
	}{
		{
			analyzer:    "droppederrors",
			wantHeader:  "# Baseline for ap droppederrors ratchet.\n# Entries: <file>:<enclosing-function>[:<category>]:<callee>\n# Do not add entries manually; fix errors or run 'ap lint droppederrors --write-baseline' if approved.\n",
			wantEntries: "a/a.go:A:write\na/a.go:Z:write\nb/b.go:B:close\n",
		},
		{
			analyzer:    "closewriter",
			wantHeader:  "# Baseline for ap closewriter ratchet.\n# Entries: <file>:<enclosing-function>[:<category>]:<detail>\n# Do not add entries manually; fix errors or run 'ap lint closewriter --write-baseline' if approved.\n",
			wantEntries: "a/a.go:A:write\na/a.go:Z:write\nb/b.go:B:close\n",
		},
	}

	entries := []Entry{
		{File: "b/b.go", EnclosingFunc: "B", Detail: "close"},
		{File: "a/a.go", EnclosingFunc: "Z", Detail: "write"},
		{File: "a/a.go", EnclosingFunc: "A", Detail: "write"},
	}

	for _, tc := range tests {
		t.Run(tc.analyzer, func(t *testing.T) {
			formatted := Format(tc.analyzer, entries)
			if !strings.HasPrefix(formatted, tc.wantHeader) {
				t.Errorf("header mismatch for %s:\ngot:\n%s\nwant prefix:\n%s", tc.analyzer, formatted, tc.wantHeader)
			}
			parsed, err := Parse([]byte(formatted))
			if err != nil {
				t.Fatalf("Parse on formatted output failed: %v", err)
			}
			if len(parsed) != 3 {
				t.Fatalf("expected 3 entries, got %d", len(parsed))
			}
			if parsed[0].File != "a/a.go" || parsed[0].EnclosingFunc != "A" {
				t.Errorf("expected a/a.go:A first, got %+v", parsed[0])
			}
			if parsed[1].File != "a/a.go" || parsed[1].EnclosingFunc != "Z" {
				t.Errorf("expected a/a.go:Z second, got %+v", parsed[1])
			}
			if parsed[2].File != "b/b.go" || parsed[2].EnclosingFunc != "B" {
				t.Errorf("expected b/b.go:B third, got %+v", parsed[2])
			}
		})
	}
}

func TestMatch_Multiset(t *testing.T) {
	tmpDir := t.TempDir()
	fooPath := filepath.Join(tmpDir, "pkg", "foo.go")
	if err := os.MkdirAll(filepath.Dir(fooPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fooPath, []byte("package foo"), 0644); err != nil {
		t.Fatal(err)
	}

	baseline := []Entry{
		{File: "pkg/foo.go", EnclosingFunc: "Run", Detail: "bar"},
		{File: "pkg/foo.go", EnclosingFunc: "Run", Detail: "bar"},
	}

	checkedFiles := map[string]bool{
		"pkg/foo.go": true,
	}

	// 1 finding: 1 matches baseline, 1 baseline entry becomes stale
	findings := []Finding{
		{
			Pos:           token.Position{Filename: fooPath, Line: 10, Column: 5},
			RelFile:       "pkg/foo.go",
			EnclosingFunc: "Run",
			Detail:        "bar",
		},
	}

	newFindings, staleEntries := Match(findings, baseline, checkedFiles, tmpDir)
	if len(newFindings) != 0 {
		t.Errorf("expected 0 new findings, got %d", len(newFindings))
	}
	if len(staleEntries) != 1 {
		t.Fatalf("expected 1 stale entry, got %d", len(staleEntries))
	}
	if staleEntries[0].File != "pkg/foo.go" || staleEntries[0].EnclosingFunc != "Run" {
		t.Errorf("unexpected stale entry: %+v", staleEntries[0])
	}

	// 3 findings: 2 match baseline, 1 is new finding, 0 stale entries
	findings = append(findings,
		Finding{
			Pos:           token.Position{Filename: fooPath, Line: 20, Column: 5},
			RelFile:       "pkg/foo.go",
			EnclosingFunc: "Run",
			Detail:        "bar",
		},
		Finding{
			Pos:           token.Position{Filename: fooPath, Line: 30, Column: 5},
			RelFile:       "pkg/foo.go",
			EnclosingFunc: "Run",
			Detail:        "bar",
		},
	)

	newFindings, staleEntries = Match(findings, baseline, checkedFiles, tmpDir)
	if len(newFindings) != 1 {
		t.Errorf("expected 1 new finding, got %d", len(newFindings))
	}
	if len(staleEntries) != 0 {
		t.Errorf("expected 0 stale entries, got %d", len(staleEntries))
	}
}

func TestMatch_UncheckedModuleNotStale(t *testing.T) {
	tmpDir := t.TempDir()
	// File in module B exists
	modBFile := filepath.Join(tmpDir, "modb", "b.go")
	if err := os.MkdirAll(filepath.Dir(modBFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modBFile, []byte("package b"), 0644); err != nil {
		t.Fatal(err)
	}

	baseline := []Entry{
		{File: "moda/a.go", EnclosingFunc: "Run", Detail: "bar"},
		{File: "modb/b.go", EnclosingFunc: "Run", Detail: "bar"}, // Not checked in this run
	}

	// We only checked moda/a.go
	checkedFiles := map[string]bool{
		"moda/a.go": true,
	}

	findings := []Finding{
		{
			Pos:           token.Position{Filename: filepath.Join(tmpDir, "moda", "a.go"), Line: 10, Column: 5},
			RelFile:       "moda/a.go",
			EnclosingFunc: "Run",
			Detail:        "bar",
		},
	}

	newFindings, staleEntries := Match(findings, baseline, checkedFiles, tmpDir)
	if len(newFindings) != 0 {
		t.Errorf("expected 0 new findings, got %d", len(newFindings))
	}
	// modb/b.go was not checked, but exists on disk -> should NOT be considered stale!
	if len(staleEntries) != 0 {
		t.Errorf("expected 0 stale entries, got %d: %+v", len(staleEntries), staleEntries)
	}
}

func TestMatch_DeletedFileIsStale(t *testing.T) {
	tmpDir := t.TempDir()
	baseline := []Entry{
		{File: "deleted/file.go", EnclosingFunc: "Run", Detail: "bar"},
	}

	// deleted/file.go does not exist on disk
	checkedFiles := map[string]bool{}

	newFindings, staleEntries := Match(nil, baseline, checkedFiles, tmpDir)
	if len(newFindings) != 0 {
		t.Errorf("expected 0 new findings, got %d", len(newFindings))
	}
	if len(staleEntries) != 1 {
		t.Fatalf("expected 1 stale entry for deleted file, got %d", len(staleEntries))
	}
	if staleEntries[0].File != "deleted/file.go" {
		t.Errorf("unexpected stale entry: %+v", staleEntries[0])
	}
}

func TestResolveBaselinePath(t *testing.T) {
	tmpDir := t.TempDir()
	apDir := filepath.Join(tmpDir, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}

	analyzers := []string{"droppederrors", "closewriter", "unused"}
	for _, a := range analyzers {
		// When no file exists and writeBaseline is false -> returns empty string
		p := ResolveBaselinePath(tmpDir, tmpDir, a, "", false)
		if p != "" {
			t.Errorf("expected empty path for %s when file does not exist, got %s", a, p)
		}

		// When writeBaseline is true -> returns default path in root/.ap/<analyzer>-baseline.txt
		expectedDefault := filepath.Join(apDir, a+"-baseline.txt")
		pWrite := ResolveBaselinePath(tmpDir, tmpDir, a, "", true)
		if pWrite != expectedDefault {
			t.Errorf("expected default write path %s for %s, got %s", expectedDefault, a, pWrite)
		}

		// When file exists on disk -> returns default path even if writeBaseline is false
		if err := os.WriteFile(expectedDefault, []byte(""), 0644); err != nil {
			t.Fatal(err)
		}
		pExisting := ResolveBaselinePath(tmpDir, tmpDir, a, "", false)
		if pExisting != expectedDefault {
			t.Errorf("expected existing path %s for %s, got %s", expectedDefault, a, pExisting)
		}

		// Configured path overrides default
		custom := "custom-" + a + ".txt"
		pConfigured := ResolveBaselinePath(tmpDir, tmpDir, a, custom, false)
		expectedCustom := filepath.Join(tmpDir, custom)
		if pConfigured != expectedCustom {
			t.Errorf("expected configured path %s for %s, got %s", expectedCustom, a, pConfigured)
		}
	}
}

type captureHandler struct {
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *captureHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(_ string) slog.Handler      { return h }

func TestApply_WarnMode(t *testing.T) {
	handler := &captureHandler{}
	logger := slog.New(handler)
	ctx := scoped.WithLog(t.Context(), logger)

	var stderr bytes.Buffer
	findings := []Finding{
		{
			Pos: token.Position{
				Filename: "pkg/foo/bar.go",
				Line:     10,
				Column:   5,
			},
			Message:       "use t.Context() instead",
			RelFile:       "pkg/foo/bar.go",
			EnclosingFunc: "TestFoo",
			Related: []RelatedInfo{
				{
					Pos:     token.Position{Filename: "pkg/foo/bar.go", Line: 5, Column: 2},
					Message: "context created here",
				},
			},
		},
	}

	opts := ApplyOptions{
		Name:            "testcontext",
		RepoRoot:        "/repo",
		Dir:             "/repo",
		AllCheckedFiles: map[string]bool{"pkg/foo/bar.go": true},
		Mode:            "warn",
		Stderr:          &stderr,
	}

	reported, err := Apply(ctx, findings, opts)
	if err != nil {
		t.Fatalf("unexpected error in warn mode: %v", err)
	}
	if len(reported) != 1 {
		t.Fatalf("expected 1 reported finding, got %d", len(reported))
	}

	// In warn mode, findings must not be written to Stderr (avoiding duplicates)
	if stderr.Len() != 0 {
		t.Errorf("expected empty stderr in warn mode, got:\n%s", stderr.String())
	}

	// Must log each finding, related info, and summary at warning level
	if len(handler.records) != 3 {
		t.Fatalf("expected 3 log records, got %d", len(handler.records))
	}

	// 1. Finding logged at warning level in file:line: message form
	if handler.records[0].Level != slog.LevelWarn {
		t.Errorf("record 0: expected LevelWarn, got %v", handler.records[0].Level)
	}
	if want := "pkg/foo/bar.go:10:5: use t.Context() instead"; handler.records[0].Message != want {
		t.Errorf("record 0: message = %q, want %q", handler.records[0].Message, want)
	}

	// 2. Related info line logged at warning level
	if handler.records[1].Level != slog.LevelWarn {
		t.Errorf("record 1: expected LevelWarn, got %v", handler.records[1].Level)
	}
	if want := "pkg/foo/bar.go:5:2:\tcontext created here"; handler.records[1].Message != want {
		t.Errorf("record 1: message = %q, want %q", handler.records[1].Message, want)
	}

	// 3. Summary line logged at warning level
	if handler.records[2].Level != slog.LevelWarn {
		t.Errorf("record 2: expected LevelWarn, got %v", handler.records[2].Level)
	}
	if want := "check failed"; handler.records[2].Message != want {
		t.Errorf("record 2: message = %q, want %q", handler.records[2].Message, want)
	}

	var hasName, hasDiagnostics bool
	handler.records[2].Attrs(func(a slog.Attr) bool {
		if a.Key == "name" && a.Value.String() == "testcontext" {
			hasName = true
		}
		if a.Key == "diagnostics" && a.Value.Int64() == 1 {
			hasDiagnostics = true
		}
		return true
	})
	if !hasName {
		t.Errorf("record 2: missing name=testcontext attribute")
	}
	if !hasDiagnostics {
		t.Errorf("record 2: missing diagnostics=1 attribute")
	}
}

func TestApply_ErrorMode(t *testing.T) {
	handler := &captureHandler{}
	logger := slog.New(handler)
	ctx := scoped.WithLog(t.Context(), logger)

	var stderr bytes.Buffer
	findings := []Finding{
		{
			Pos: token.Position{
				Filename: "pkg/foo/bar.go",
				Line:     10,
				Column:   5,
			},
			Message:       "unused variable",
			RelFile:       "pkg/foo/bar.go",
			EnclosingFunc: "TestFoo",
		},
	}

	opts := ApplyOptions{
		Name:            "unused",
		RepoRoot:        "/repo",
		Dir:             "/repo",
		AllCheckedFiles: map[string]bool{"pkg/foo/bar.go": true},
		Mode:            "error",
		Stderr:          &stderr,
	}

	_, err := Apply(ctx, findings, opts)
	if err == nil {
		t.Fatal("expected error in error mode, got nil")
	}

	// In error mode, findings must be written to Stderr and NOT logged at warning level
	if !strings.Contains(stderr.String(), "pkg/foo/bar.go:10:5: unused variable") {
		t.Errorf("expected finding in stderr, got:\n%s", stderr.String())
	}
	if len(handler.records) != 0 {
		t.Errorf("expected no warning log records in error mode, got %d", len(handler.records))
	}
}
