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

func TestApply_Reporting(t *testing.T) {
	finding := Finding{
		Pos:     token.Position{Filename: "pkg/foo/bar.go", Line: 10, Column: 5},
		Message: "use t.Context() instead",
		Related: []RelatedInfo{{Pos: token.Position{Filename: "pkg/foo/bar.go", Line: 5, Column: 2}, Message: "context created here"}},
	}

	tests := []struct {
		mode       string
		wantErr    bool
		wantLog    []string
		wantStderr string
	}{
		{
			mode:    "warn",
			wantErr: false,
			wantLog: []string{
				"level=WARN msg=\"pkg/foo/bar.go:10:5: use t.Context() instead\"",
				"level=WARN msg=\"pkg/foo/bar.go:5:2:\\tcontext created here\"",
				"level=WARN msg=\"check failed\" name=testcontext dir=/repo diagnostics=1",
			},
		},
		{
			mode:       "error",
			wantErr:    true,
			wantStderr: "pkg/foo/bar.go:10:5: use t.Context() instead\npkg/foo/bar.go:5:2:\tcontext created here\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.mode, func(t *testing.T) {
			var logBuf, errBuf bytes.Buffer
			ctx := scoped.WithLog(t.Context(), slog.New(slog.NewTextHandler(&logBuf, nil)))
			opts := ApplyOptions{Name: "testcontext", RepoRoot: "/repo", Dir: "/repo", Mode: tc.mode, Stderr: &errBuf}
			_, err := Apply(ctx, []Finding{finding}, opts)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Apply() error = %v, wantErr %v", err, tc.wantErr)
			}
			for _, w := range tc.wantLog {
				if !strings.Contains(logBuf.String(), w) {
					t.Errorf("log output missing %q, got:\n%s", w, logBuf.String())
				}
			}
			if len(tc.wantLog) == 0 && logBuf.Len() != 0 {
				t.Errorf("expected no log output, got:\n%s", logBuf.String())
			}
			if errBuf.String() != tc.wantStderr {
				t.Errorf("stderr = %q, want %q", errBuf.String(), tc.wantStderr)
			}
		})
	}
}
