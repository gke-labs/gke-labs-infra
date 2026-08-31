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
	"os"
	"path/filepath"
	"strings"
	"testing"
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
				if err := os.WriteFile(filepath.Join(root, ".ap", "ap.yaml"), []byte(tc.apYAML+"\n"), 0644); err != nil {
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
				if err := os.WriteFile(filepath.Join(root, ".ap", "ap.yaml"), []byte(tc.apYAML+"\n"), 0644); err != nil {
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
	if err := os.WriteFile(filepath.Join(root, ".ap", "headers.yaml"), []byte(headersYAML), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestPresubmitScriptsWithoutSkipConfig(t *testing.T) {
	root := t.TempDir()
	writeTestHeadersConfig(t, root)
	if err := runApTestGenerator(t.Context(), root); err != nil {
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
	if err := os.WriteFile(filepath.Join(root, ".ap", "ap.yaml"), []byte(apYAML), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runApTestGenerator(t.Context(), root); err != nil {
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
