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

package walker

import (
	"strings"
	"testing"
)

func TestIgnoreList(t *testing.T) {
	grid := []struct {
		Pattern    string
		Matches    []string
		NonMatches []string
	}{
		{
			Pattern: "third_party/",
			Matches: []string{
				"third_party/",
				"src/third_party/",
			},
			NonMatches: []string{
				"third_party", // file
				"foo/bar",
			},
		},
		{
			Pattern: "**/testdata/",
			Matches: []string{
				"testdata/",
				"pkg/testdata/",
				"pkg/sub/testdata/",
			},
			NonMatches: []string{
				"testdata",
				"pkg/testdata", // file
				"pkg/not_testdata/",
			},
		},
		{
			Pattern: "*_generated.go",
			Matches: []string{
				"foo_generated.go",
				"pkg/bar_generated.go",
			},
			NonMatches: []string{
				"generated.go.txt",
				"foo_generated",
			},
		},
		{
			Pattern: "*.yaml",
			Matches: []string{
				"file.yaml",
				"foo/bar/file.yaml",
			},
			NonMatches: []string{
				"file.yml",
			},
		},
		{
			Pattern: "vendor",
			Matches: []string{
				"vendor",
				"vendor/",
				"pkg/vendor",
				"pkg/vendor/",
			},
			NonMatches: []string{
				"vendor_foo",
			},
		},
		{
			Pattern: "foo/bar",
			Matches: []string{
				"foo/bar",
				"foo/bar/",
			},
			NonMatches: []string{
				"src/foo/bar",
			},
		},
	}

	for _, g := range grid {
		l := NewIgnoreList([]string{g.Pattern})

		for _, path := range g.Matches {
			isDir := strings.HasSuffix(path, "/")
			checkPath := strings.TrimSuffix(path, "/")
			if !l.ShouldIgnore(checkPath, isDir) {
				t.Errorf("Pattern %q should match %q (isDir=%v)", g.Pattern, checkPath, isDir)
			}
		}

		for _, path := range g.NonMatches {
			isDir := strings.HasSuffix(path, "/")
			checkPath := strings.TrimSuffix(path, "/")
			if l.ShouldIgnore(checkPath, isDir) {
				t.Errorf("Pattern %q should NOT match %q (isDir=%v)", g.Pattern, checkPath, isDir)
			}
		}
	}
}
