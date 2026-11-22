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
	"strings"
	"testing"
)

// TestActionsVersionsMatchDependabotFile verifies that the action versions
// emitted by the generator match the action.yml file that Dependabot updates.
func TestActionsVersionsMatchDependabotFile(t *testing.T) {
	diskContent, err := os.ReadFile("action.yml")
	if err != nil {
		t.Fatalf("failed to read action.yml: %v", err)
	}

	diskVersions, err := parseActionVersions(diskContent)
	if err != nil {
		t.Fatalf("failed to parse action.yml from disk: %v", err)
	}

	if actions != diskVersions {
		t.Fatalf("generator action versions mismatch with action.yml on disk:\nembedded: %+v\ndisk:     %+v", actions, diskVersions)
	}
}

func TestParseActionVersions_Errors(t *testing.T) {
	validCheckout := "    - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # ratchet:actions/checkout@v7.0.1\n"
	validSetupGo := "    - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # ratchet:actions/setup-go@v7.0.0\n"
	validUploadArtifact := "    - uses: actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # ratchet:actions/upload-artifact@v7.0.2\n"
	validCacheRestore := "    - uses: actions/cache/restore@55cc8345863c7cc4c66a329aec7e433d2d1c52a9 # ratchet:actions/cache/restore@v6.1.0\n"
	validCacheSave := "    - uses: actions/cache/save@55cc8345863c7cc4c66a329aec7e433d2d1c52a9 # ratchet:actions/cache/save@v6.1.0\n"

	allValid := validCheckout + validSetupGo + validUploadArtifact + validCacheRestore + validCacheSave

	cases := []struct {
		name      string
		content   string
		errSubstr string
	}{
		{"missing checkout", validSetupGo + validUploadArtifact + validCacheRestore + validCacheSave, "missing action actions/checkout"},
		{"missing setup-go", validCheckout + validUploadArtifact + validCacheRestore + validCacheSave, "missing action actions/setup-go"},
		{"missing upload-artifact", validCheckout + validSetupGo + validCacheRestore + validCacheSave, "missing action actions/upload-artifact"},
		{"missing cache-restore", validCheckout + validSetupGo + validUploadArtifact + validCacheSave, "missing action actions/cache/restore"},
		{"missing cache-save", validCheckout + validSetupGo + validUploadArtifact + validCacheRestore, "missing action actions/cache/save"},
		{"empty uses", allValid + "    - uses:\n", "empty uses reference"},
		{"missing @", allValid + "    - uses: actions/checkout\n", "missing '@'"},
		{"short SHA", allValid + "    - uses: actions/checkout@1234 # ratchet:actions/checkout@v7.0.1\n", "not pinned to a full 40-char commit SHA"},
		{"missing ratchet comment", allValid + "    - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1\n", "missing ratchet comment"},
		{"unknown action", allValid + "    - uses: other/action@3d3c42e5aac5ba805825da76410c181273ba90b1 # ratchet:other/action@v1\n", "unknown action"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseActionVersions([]byte(tc.content)); err == nil || !strings.Contains(err.Error(), tc.errSubstr) {
				t.Fatalf("parseActionVersions() error = %v, want substring %q", err, tc.errSubstr)
			}
		})
	}
}
