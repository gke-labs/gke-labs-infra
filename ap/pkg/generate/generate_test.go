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

package generate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/repo"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

func TestGetApCommand(t *testing.T) {
	cases := []struct {
		name   string
		apYAML string // empty = no .ap/ap.yaml
		want   string
	}{
		{
			name: "no config file",
			want: "go run github.com/gke-labs/gke-labs-infra/ap@latest",
		},
		{
			name:   "self",
			apYAML: `version: "!self"`,
			want:   "go run ./ap",
		},
		{
			name:   "latest",
			apYAML: `version: latest`,
			want:   "go run github.com/gke-labs/gke-labs-infra/ap@latest",
		},
		{
			name:   "empty version",
			apYAML: `version: ""`,
			want:   "go run github.com/gke-labs/gke-labs-infra/ap@latest",
		},
		{
			name:   "pinned release",
			apYAML: `version: v0.12.3`,
			want:   "go run github.com/gke-labs/gke-labs-infra/ap@v0.12.3",
		},
		{
			name:   "pinned pseudo-version",
			apYAML: `version: v0.0.0-20260718102101-abcdef123456`,
			want:   "go run github.com/gke-labs/gke-labs-infra/ap@v0.0.0-20260718102101-abcdef123456",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.apYAML != "" {
				if err := os.MkdirAll(filepath.Join(root, ".ap"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := fileutils.WriteFileAtomic(filepath.Join(root, ".ap", "ap.yaml"), []byte(tc.apYAML+"\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := GetApCommand(root, root)
			if err != nil {
				t.Fatalf("GetApCommand: %v", err)
			}
			if got != tc.want {
				t.Errorf("GetApCommand = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLoadPresubmitSkipPatterns(t *testing.T) {
	cases := []struct {
		name    string
		apYAML  string // empty = no .ap/ap.yaml
		want    []string
		wantErr bool
	}{
		{
			name: "no config file",
			want: nil,
		},
		{
			name:   "config without presubmits",
			apYAML: `version: latest`,
			want:   nil,
		},
		{
			name: "patterns",
			apYAML: `version: latest
presubmits:
  skipIfOnlyChanged:
  - docs/*
  - README.md
  - "*.md"`,
			want: []string{"docs/*", "README.md", "*.md"},
		},
		{
			name: "rejects shell metacharacters",
			apYAML: `presubmits:
  skipIfOnlyChanged:
  - "docs/*;rm -rf"`,
			wantErr: true,
		},
		{
			name: "rejects empty pattern",
			apYAML: `presubmits:
  skipIfOnlyChanged:
  - ""`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.apYAML != "" {
				if err := os.MkdirAll(filepath.Join(root, ".ap"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := fileutils.WriteFileAtomic(filepath.Join(root, ".ap", "ap.yaml"), []byte(tc.apYAML+"\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := loadPresubmitSkipPatterns(root)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("loadPresubmitSkipPatterns = %v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadPresubmitSkipPatterns: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("loadPresubmitSkipPatterns = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("pattern[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestPresubmitScriptsWithoutSkipConfig guards byte-stability for
// repos that don't opt in: without presubmits.skipIfOnlyChanged the
// generated scripts must not contain the guard, so existing consumer
// repos see no diff from ap-verify-generate.
// writeTestHeadersConfig writes the minimal .ap/headers.yaml the
// script generators need to render file headers.
func writeTestHeadersConfig(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	headersYAML := "license: apache-2.0\ncopyrightHolder: Google LLC\n"
	if err := fileutils.WriteFileAtomic(filepath.Join(root, ".ap", "headers.yaml"), []byte(headersYAML), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestPresubmitScriptsWithoutSkipConfig(t *testing.T) {
	root := t.TempDir()
	writeTestHeadersConfig(t, root)
	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := runApTestGenerator(t.Context(), r, root); err != nil {
		t.Fatalf("runApTestGenerator: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "dev", "ci", "presubmits", "ap-test"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "skipIfOnlyChanged") {
		t.Errorf("guard emitted without config:\n%s", b)
	}
	if !strings.Contains(string(b), "cd \"${REPO_ROOT}\"\n\n# Run tests") {
		t.Errorf("expected original spacing preserved; got:\n%s", b)
	}
}

func TestPresubmitScriptsWithSkipConfig(t *testing.T) {
	root := t.TempDir()
	writeTestHeadersConfig(t, root)
	apYAML := `presubmits:
  skipIfOnlyChanged:
  - docs/*
  - README.md
`
	if err := fileutils.WriteFileAtomic(filepath.Join(root, ".ap", "ap.yaml"), []byte(apYAML), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := runApTestGenerator(t.Context(), r, root); err != nil {
		t.Fatalf("runApTestGenerator: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "dev", "ci", "presubmits", "ap-test"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	if !strings.Contains(script, "docs/*|README.md) ;;") {
		t.Errorf("expected case patterns in guard; got:\n%s", script)
	}
	if !strings.Contains(script, `if [[ "${GITHUB_EVENT_NAME:-}" == "pull_request"`) {
		t.Errorf("expected pull_request gate in guard; got:\n%s", script)
	}
	// The guard must precede the actual work so the skip avoids it.
	if guardIdx, runIdx := strings.Index(script, "skipIfOnlyChanged"), strings.Index(script, "# Run tests"); guardIdx < 0 || runIdx < 0 || guardIdx > runIdx {
		t.Errorf("guard not placed before the test invocation:\n%s", script)
	}
}

// TestPinnedActionRefs guards the org hash-pin policy: generated
// workflows must reference actions by full commit SHA, never by a
// mutable tag.
func TestPinnedActionRefs(t *testing.T) {
	for _, ref := range []string{actionCheckout, actionSetupGo, actionUploadArtifact} {
		at := strings.Index(ref, "@")
		if at < 0 {
			t.Errorf("action ref %q has no @", ref)
			continue
		}
		rest := ref[at+1:]
		sha, _, _ := strings.Cut(rest, " ")
		if len(sha) != 40 {
			t.Errorf("action ref %q is not pinned to a full 40-char commit SHA (got %q)", ref, sha)
		}
		if !strings.Contains(rest, "# ratchet:") {
			t.Errorf("action ref %q missing ratchet version comment", ref)
		}
	}
}

func TestGithubActionsWorkflowArtifactUpload(t *testing.T) {
	root := t.TempDir()
	writeTestHeadersConfig(t, root)

	presubmitsDir := filepath.Join(root, "dev", "ci", "presubmits")
	if err := os.MkdirAll(presubmitsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create presubmit scripts: one without test/e2e in name (e.g. ap-posix or ap-lint), one with.
	for _, script := range []string{"ap-posix", "ap-test"} {
		path := filepath.Join(presubmitsDir, script)
		if err := fileutils.WriteFileAtomic(path, []byte("#!/bin/bash\nexit 0\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}

	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	scopes := []*tasks.APScope{{Dir: root, RepoRoot: root, Repo: r}}
	if err := runGithubActionsGenerator(t.Context(), r, root, scopes); err != nil {
		t.Fatalf("runGithubActionsGenerator: %v", err)
	}

	workflowPath := filepath.Join(root, ".github", "workflows", "ci-presubmits.yaml")
	b, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(b)

	for _, job := range []string{"ap-posix", "ap-test"} {
		expectedStep := fmt.Sprintf(`      - name: Upload artifacts
        if: always()
        uses: %s
        with:
          name: artifacts-%s
          path: /tmp/artifacts
          if-no-files-found: ignore`, actionUploadArtifact, job)
		if !strings.Contains(content, expectedStep) {
			t.Errorf("job %s missing expected artifact upload step in workflow:\n%s", job, content)
		}
	}
}

func TestPresubmitScriptsWithoutHeadersConfig(t *testing.T) {
	root := t.TempDir()
	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := runApTestGenerator(t.Context(), r, root); err != nil {
		t.Fatalf("runApTestGenerator should succeed without headers.yaml: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "dev", "ci", "presubmits", "ap-test"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	if !strings.HasPrefix(script, "#!/bin/bash\n# Code generated by ap generate. DO NOT EDIT.\n\nset -o errexit\n") {
		t.Errorf("expected script without header to start directly with shebang and set -o errexit, got:\n%s", script)
	}
}

func TestPresubmitScriptsWithLicenseNone(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := fileutils.WriteFileAtomic(filepath.Join(root, ".ap", "headers.yaml"), []byte("license: none\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := runApTestGenerator(t.Context(), r, root); err != nil {
		t.Fatalf("runApTestGenerator should succeed with license: none: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "dev", "ci", "presubmits", "ap-test"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	if !strings.HasPrefix(script, "#!/bin/bash\n# Code generated by ap generate. DO NOT EDIT.\n\nset -o errexit\n") {
		t.Errorf("expected script with license: none to start directly with shebang and set -o errexit, got:\n%s", script)
	}
}

func TestGenerateTasks_DevTasks(t *testing.T) {
	root := t.TempDir()
	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	scopes := []*tasks.APScope{{Dir: root, RepoRoot: root, Repo: r}}

	// 1. dev/tasks does not exist -> should succeed
	task, err := GenerateTasks(root, scopes)
	if err != nil {
		t.Fatalf("unexpected error when dev/tasks does not exist: %v", err)
	}
	if task == nil {
		t.Fatal("expected non-nil task")
	}

	// 2. dev/tasks has a script -> should succeed
	tasksDir := filepath.Join(root, "dev", "tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := fileutils.WriteFileAtomic(filepath.Join(tasksDir, "generate-foo"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	repo.ResetCache()
	r, err = repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	scopes = []*tasks.APScope{{Dir: root, RepoRoot: root, Repo: r}}

	task, err = GenerateTasks(root, scopes)
	if err != nil {
		t.Fatalf("unexpected error with dev/tasks script: %v", err)
	}
	if task == nil {
		t.Fatal("expected non-nil task")
	}
}

func TestGenerateIdempotence(t *testing.T) {
	root := t.TempDir()

	// Set up a repo with a Go module and no generated presubmit scripts
	if err := fileutils.WriteFileAtomic(filepath.Join(root, "go.mod"), []byte("module example.com/test\n\ngo 1.26\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := fileutils.WriteFileAtomic(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	writeTestHeadersConfig(t, root)

	// Add one hand-written presubmit script
	presubmitsDir := filepath.Join(root, "dev", "ci", "presubmits")
	if err := os.MkdirAll(presubmitsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := fileutils.WriteFileAtomic(filepath.Join(presubmitsDir, "check-foo"), []byte("#!/bin/bash\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}

	snapshot := func() map[string]string {
		files := make(map[string]string)
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() {
				rel, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				files[rel] = string(data)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return files
	}

	// Run 1: initial generate
	repo.ResetCache()
	r1, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	scope1 := &tasks.APScope{Dir: root, RepoRoot: root, Repo: r1}
	task1, err := GenerateTasks(root, []*tasks.APScope{scope1})
	if err != nil {
		t.Fatalf("GenerateTasks run 1 failed: %v", err)
	}
	if err := task1.Run(t.Context(), scope1); err != nil {
		t.Fatalf("task1.Run failed: %v", err)
	}

	snap1 := snapshot()

	// Verify that run 1 produced ci-presubmits.yaml containing the generated presubmits
	workflow, ok := snap1[filepath.Join(".github", "workflows", "ci-presubmits.yaml")]
	if !ok {
		t.Fatal("expected .github/workflows/ci-presubmits.yaml to be generated in run 1")
	}
	for _, expectedJob := range []string{"check-foo", "ap-verify-generate", "ap-test", "ap-lint"} {
		if !strings.Contains(workflow, expectedJob) {
			t.Errorf("run 1 workflow missing job %q", expectedJob)
		}
	}

	// Run 2: generate again
	repo.ResetCache()
	r2, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	scope2 := &tasks.APScope{Dir: root, RepoRoot: root, Repo: r2}
	task2, err := GenerateTasks(root, []*tasks.APScope{scope2})
	if err != nil {
		t.Fatalf("GenerateTasks run 2 failed: %v", err)
	}
	if err := task2.Run(t.Context(), scope2); err != nil {
		t.Fatalf("task2.Run failed: %v", err)
	}

	snap2 := snapshot()

	// Assert nothing changed between run 1 and run 2
	if len(snap1) != len(snap2) {
		t.Fatalf("file count mismatch: run 1 had %d files, run 2 had %d files", len(snap1), len(snap2))
	}
	for file, content1 := range snap1 {
		content2, ok := snap2[file]
		if !ok {
			t.Errorf("file %s was removed in run 2", file)
			continue
		}
		if content1 != content2 {
			t.Errorf("file %s changed between run 1 and run 2:\n--- run 1 ---\n%s\n--- run 2 ---\n%s", file, content1, content2)
		}
	}
	for file := range snap2 {
		if _, ok := snap1[file]; !ok {
			t.Errorf("file %s was added in run 2", file)
		}
	}
}

func TestAuditGeneratedGeneratorOptIn(t *testing.T) {
	root := t.TempDir()
	writeTestHeadersConfig(t, root)

	scriptPath := filepath.Join(root, "dev", "ci", "presubmits", "ap-audit-generated")
	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Without auditGenerated configured: script should not exist
	if err := runAuditGeneratedGenerator(t.Context(), r, root); err != nil {
		t.Fatalf("runAuditGeneratedGenerator failed: %v", err)
	}
	if _, err := os.Stat(scriptPath); err == nil {
		t.Fatalf("expected ap-audit-generated NOT to exist when not configured")
	}

	// 2. With auditGenerated: true: script should be generated
	apYAML := `version: "!self"
generate:
  auditGenerated: true
`
	if err := os.WriteFile(filepath.Join(root, ".ap", "ap.yaml"), []byte(apYAML), 0644); err != nil {
		t.Fatal(err)
	}

	if err := runAuditGeneratedGenerator(t.Context(), r, root); err != nil {
		t.Fatalf("runAuditGeneratedGenerator with auditGenerated=true failed: %v", err)
	}

	b, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("expected ap-audit-generated to be generated: %v", err)
	}
	content := string(b)
	if !strings.Contains(content, "ap generate --audit-generated --strict //...") {
		t.Errorf("expected script to run ap generate --audit-generated --strict //..., got:\n%s", content)
	}

	// 3. When auditGenerated is disabled, script is cleaned up
	apYAMLDisabled := `version: "!self"
generate:
  auditGenerated: false
`
	if err := os.WriteFile(filepath.Join(root, ".ap", "ap.yaml"), []byte(apYAMLDisabled), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runAuditGeneratedGenerator(t.Context(), r, root); err != nil {
		t.Fatalf("runAuditGeneratedGenerator with auditGenerated=false failed: %v", err)
	}
	if _, err := os.Stat(scriptPath); err == nil {
		t.Fatalf("expected ap-audit-generated to be removed when disabled")
	}
}
