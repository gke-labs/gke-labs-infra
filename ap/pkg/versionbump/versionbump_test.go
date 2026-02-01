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

package versionbump

import (
	"testing"
)

func TestBumpContent(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		content  string
		version  string
		want     string
		changed  bool
	}{
		{
			name:     "go.mod exact version",
			filename: "go.mod",
			content:  "module foo\n\ngo 1.23.0\n",
			version:  "1.25.6",
			want:     "module foo\n\ngo 1.25.6\n",
			changed:  true,
		},
		{
			name:     "go.mod major minor",
			filename: "go.mod",
			content:  "module foo\n\ngo 1.23\n",
			version:  "1.25.6",
			want:     "module foo\n\ngo 1.25.6\n",
			changed:  true,
		},
		{
			name:     "go.mod already updated",
			filename: "go.mod",
			content:  "module foo\n\ngo 1.25.6\n",
			version:  "1.25.6",
			want:     "module foo\n\ngo 1.25.6\n",
			changed:  false,
		},
		{
			name:     "Dockerfile with suffix",
			filename: "Dockerfile",
			content:  "FROM golang:1.24-bookworm\n",
			version:  "1.25.6",
			want:     "FROM golang:1.25.6-bookworm\n",
			changed:  true,
		},
		{
			name:     "Dockerfile with trixie suffix",
			filename: "Dockerfile.foo",
			content:  "FROM golang:1.25.1-trixie AS build\n",
			version:  "1.25.6",
			want:     "FROM golang:1.25.6-trixie AS build\n",
			changed:  true,
		},
		{
			name:     "Dockerfile no suffix",
			filename: "Dockerfile",
			content:  "FROM golang:1.24\n",
			version:  "1.25.6",
			want:     "FROM golang:1.25.6\n",
			changed:  true,
		},
		{
			name:     "Dockerfile multiple occurrences",
			filename: "Dockerfile",
			content:  "FROM golang:1.24 AS build\nRUN echo hi\nFROM golang:1.24-bookworm\n",
			version:  "1.25.6",
			want:     "FROM golang:1.25.6 AS build\nRUN echo hi\nFROM golang:1.25.6-bookworm\n",
			changed:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := bumpContent(tt.filename, []byte(tt.content), tt.version)
			if string(got) != tt.want {
				t.Errorf("bumpContent() got = %v, want %v", string(got), tt.want)
			}
			if changed != tt.changed {
				t.Errorf("bumpContent() changed = %v, want %v", changed, tt.changed)
			}
		})
	}
}
