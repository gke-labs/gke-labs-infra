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

package portability

import (
	"testing"
)

func TestPortabilityFindings(t *testing.T) {
	tests := []struct {
		name            string
		line            string
		wantConstruct   string
		wantAlternative string
	}{
		// Positive cases: one per construct
		{
			name:            "sed -i not followed by suffix",
			line:            "sed -i 's/foo/bar/' file",
			wantConstruct:   "sed -i",
			wantAlternative: "use sed -i.bak ... && rm or write to a temp file; GNU and BSD disagree on -i",
		},
		{
			name:            "date -d (GNU only)",
			line:            "date -d @12345",
			wantConstruct:   "date -d",
			wantAlternative: "GNU only; compute dates in Go or use date -u +%s arithmetic",
		},
		{
			name:            "stat -c (GNU only)",
			line:            "stat -c %s file",
			wantConstruct:   "stat -c",
			wantAlternative: "GNU only; use wc -c <file for sizes, or Go",
		},
		{
			name:            "grep -P (not portable)",
			line:            "grep -P 'pattern' file",
			wantConstruct:   "grep -P",
			wantAlternative: "not portable; use grep -E",
		},
		{
			name:            "find ... -printf (GNU only)",
			line:            "find . -type f -printf '%p\\n'",
			wantConstruct:   "find ... -printf",
			wantAlternative: "GNU only",
		},
		{
			name:            "xargs -r (GNU only)",
			line:            "xargs -r rm",
			wantConstruct:   "xargs -r",
			wantAlternative: "GNU only; BSD xargs already does nothing on empty input",
		},
		{
			name:            "timeout as a command",
			line:            "timeout 10s cmd",
			wantConstruct:   "timeout",
			wantAlternative: "absent on macOS; use go test -timeout or Go",
		},
		{
			name:            "mapfile (bash 4+)",
			line:            "mapfile -t lines < file",
			wantConstruct:   "mapfile",
			wantAlternative: "bash 4+; macOS /bin/bash is 3.2",
		},
		{
			name:            "readarray (bash 4+)",
			line:            "readarray -t lines < file",
			wantConstruct:   "readarray",
			wantAlternative: "bash 4+; macOS /bin/bash is 3.2",
		},
		{
			name:            "declare -A (bash 4+)",
			line:            "declare -A my_map",
			wantConstruct:   "declare -A",
			wantAlternative: "bash 4+; macOS /bin/bash is 3.2",
		},
		{
			name:            "${name,,} (bash 4+)",
			line:            "echo ${name,,}",
			wantConstruct:   "${name,,}",
			wantAlternative: "bash 4+; macOS /bin/bash is 3.2",
		},
		{
			name:            "${name^^} (bash 4+)",
			line:            "echo ${name^^}",
			wantConstruct:   "${name^^}",
			wantAlternative: "bash 4+; macOS /bin/bash is 3.2",
		},
		// Negative cases
		{
			name: "commented-out sed -i",
			line: "# sed -i 's/foo/bar/' file",
		},
		{
			name: "sed -i.bak explicit suffix",
			line: "sed -i.bak 's/foo/bar/' file",
		},
		{
			name: "go test -timeout flag",
			line: "go test -timeout 30m ./tests/e2e -v -tags e2e",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			findings := portabilityFindings("dev/tasks/script", []string{tt.line})
			if tt.wantConstruct == "" {
				if len(findings) != 0 {
					t.Fatalf("expected 0 findings for %q, got %d: %+v", tt.line, len(findings), findings)
				}
				return
			}

			if len(findings) != 1 {
				t.Fatalf("expected 1 finding for %q, got %d: %+v", tt.line, len(findings), findings)
			}
			f := findings[0]
			if f.File != "dev/tasks/script" {
				t.Errorf("File = %q, want \"dev/tasks/script\"", f.File)
			}
			if f.Line != 1 {
				t.Errorf("Line = %d, want 1", f.Line)
			}
			if f.Construct != tt.wantConstruct {
				t.Errorf("Construct = %q, want %q", f.Construct, tt.wantConstruct)
			}
			if f.Alternative != tt.wantAlternative {
				t.Errorf("Alternative = %q, want %q", f.Alternative, tt.wantAlternative)
			}
		})
	}
}
