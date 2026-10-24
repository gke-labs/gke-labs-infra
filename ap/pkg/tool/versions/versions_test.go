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

package versions

import (
	"testing"
)

func TestParse_Success(t *testing.T) {
	data := []byte(`
tools:
  - version: "2.0.0"
    assets:
      linux-amd64: sha256:1111111111111111111111111111111111111111111111111111111111111111
  - version: "1.0.0"
    assets:
      linux-amd64: sha256:2222222222222222222222222222222222222222222222222222222222222222
`)

	rels, err := Parse(data, "tools")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rels) != 2 {
		t.Fatalf("expected 2 releases, got %d", len(rels))
	}

	latest := Latest(rels)
	if latest == nil || latest.Version != "2.0.0" {
		t.Fatalf("expected latest version 2.0.0, got %v", latest)
	}

	found, ok := Lookup(rels, "1.0.0")
	if !ok || found.Version != "1.0.0" {
		t.Errorf("expected to find 1.0.0, got %v", found)
	}

	foundV, okV := Lookup(rels, "v1.0.0")
	if !okV || foundV.Version != "1.0.0" {
		t.Errorf("expected to find v1.0.0, got %v", foundV)
	}

	foundGo, okGo := Lookup(rels, "go1.0.0")
	if !okGo || foundGo.Version != "1.0.0" {
		t.Errorf("expected to find go1.0.0, got %v", foundGo)
	}

	_, okNone := Lookup(rels, "9.9.9")
	if okNone {
		t.Error("expected Lookup to return false for nonexistent version")
	}
}

func TestParse_MissingSection(t *testing.T) {
	data := []byte(`
other:
  - version: "1.0.0"
`)

	_, err := Parse(data, "tools")
	if err == nil {
		t.Fatal("expected error for missing section, got nil")
	}
}

func TestParse_StrictRejectsUnknownFields(t *testing.T) {
	data := []byte(`
tools:
  - version: "1.0.0"
    unknown_field: "bad"
    assets:
      linux-amd64: sha256:1111111111111111111111111111111111111111111111111111111111111111
`)

	_, err := Parse(data, "tools")
	if err == nil {
		t.Fatal("expected strict unmarshal to reject unknown_field, got nil")
	}
}

func TestLatest_Empty(t *testing.T) {
	if got := Latest(nil); got != nil {
		t.Errorf("expected nil for empty slice, got %v", got)
	}
}

func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"29.9.2", "29.8.2", 1},
		{"29.8.2", "29.9.2", -1},
		{"29.9.2", "29.9.2", 0},
		{"v29.9.2", "29.9.2", 0},
		{"0.38.1", "0.21.1", 1},
		{"36.2", "36.1", 1},
		{"1.0.0", "1.0", 0},
		{"1.2.3-rc1", "1.2.3", 0},
		{"go1.27.3", "go1.27.2", 1},
		{"go1.27.2", "go1.27.3", -1},
		{"go1.27.3", "1.27.3", 0},
		{"go1.26.10", "go1.27.0", -1},
	}
	for _, tc := range tests {
		got := Compare(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSortNewestFirst(t *testing.T) {
	rels := []Release{
		{Version: "28.0.1"},
		{Version: "29.9.2"},
		{Version: "29.8.2"},
	}
	SortNewestFirst(rels)
	if rels[0].Version != "29.9.2" || rels[1].Version != "29.8.2" || rels[2].Version != "28.0.1" {
		t.Errorf("unexpected sort order: %v", rels)
	}
}
