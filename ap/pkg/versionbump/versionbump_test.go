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
			version:  "1.26.1",
			want:     "module foo\n\ngo 1.26.0\n\ntoolchain go1.26.1\n",
			changed:  true,
		},
		{
			name:     "go.mod major minor",
			filename: "go.mod",
			content:  "module foo\n\ngo 1.23\n",
			version:  "1.26.1",
			want:     "module foo\n\ngo 1.26.0\n\ntoolchain go1.26.1\n",
			changed:  true,
		},
		{
			name:     "go.mod already has go version but missing toolchain",
			filename: "go.mod",
			content:  "module foo\n\ngo 1.26.0\n",
			version:  "1.26.1",
			want:     "module foo\n\ngo 1.26.0\n\ntoolchain go1.26.1\n",
			changed:  true,
		},
		{
			name:     "go.mod already updated with toolchain",
			filename: "go.mod",
			content:  "module foo\n\ngo 1.26.0\n\ntoolchain go1.26.1\n",
			version:  "1.26.1",
			want:     "module foo\n\ngo 1.26.0\n\ntoolchain go1.26.1\n",
			changed:  false,
		},
		{
			name:     "go.mod already has old toolchain",
			filename: "go.mod",
			content:  "module foo\n\ngo 1.23.0\n\ntoolchain go1.23.0\n",
			version:  "1.26.1",
			want:     "module foo\n\ngo 1.26.0\n\ntoolchain go1.26.1\n",
			changed:  true,
		},
		{
			name:     "Dockerfile with suffix",
			filename: "Dockerfile",
			content:  "FROM golang:1.24-bookworm\n",
			version:  "1.26.1",
			want:     "FROM golang:1.26.1-bookworm\n",
			changed:  true,
		},
		{
			name:     "Dockerfile with trixie suffix",
			filename: "Dockerfile.foo",
			content:  "FROM golang:1.25.1-trixie AS build\n",
			version:  "1.26.1",
			want:     "FROM golang:1.26.1-trixie AS build\n",
			changed:  true,
		},
		{
			name:     "Dockerfile no suffix",
			filename: "Dockerfile",
			content:  "FROM golang:1.24\n",
			version:  "1.26.1",
			want:     "FROM golang:1.26.1\n",
			changed:  true,
		},
		{
			name:     "Dockerfile multiple occurrences",
			filename: "Dockerfile",
			content:  "FROM golang:1.24 AS build\nRUN echo hi\nFROM golang:1.24-bookworm\n",
			version:  "1.26.1",
			want:     "FROM golang:1.26.1 AS build\nRUN echo hi\nFROM golang:1.26.1-bookworm\n",
			changed:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := bumpContent(t.Context(), tt.filename, []byte(tt.content), tt.version)
			if string(got) != tt.want {
				t.Errorf("bumpContent() got = %v, want %v", string(got), tt.want)
			}
			if changed != tt.changed {
				t.Errorf("bumpContent() changed = %v, want %v", changed, tt.changed)
			}
		})
	}
}

func TestGetGoDirectiveVersion(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"1.26.1", "1.26.0"},
		{"1.26.0", "1.26"},
		{"1.26", "1.26"},
		{"1.26.invalid", "1.26"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := getGoDirectiveVersion(t.Context(), tt.input)
			if got != tt.want {
				t.Errorf("getGoDirectiveVersion(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsValidVersion(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"1.23", true},
		{"1.26.1", true},
		{"1.27.0", true},
		{"0.1", true},
		{"1", false},
		{"1.2.3.4", false},
		{"1.23a", false},
		{"1..0", false},
		{".1.0", false},
		{"1.0.", false},
		{"", false},
		{"latest", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isValidVersion(tt.input)
			if got != tt.expected {
				t.Errorf("isValidVersion(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestBumpGoMod(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		version     string
		wantContent string
		wantChanged bool
	}{
		{
			name:        "update go directive and add toolchain",
			content:     "module test\n\ngo 1.23.0\n",
			version:     "1.26.1",
			wantContent: "module test\n\ngo 1.26.0\n\ntoolchain go1.26.1\n",
			wantChanged: true,
		},
		{
			name:        "update existing go and toolchain",
			content:     "module test\n\ngo 1.23.0\n\ntoolchain go1.23.0\n",
			version:     "1.26.1",
			wantContent: "module test\n\ngo 1.26.0\n\ntoolchain go1.26.1\n",
			wantChanged: true,
		},
		{
			name:        "already up to date",
			content:     "module test\n\ngo 1.26.0\n\ntoolchain go1.26.1\n",
			version:     "1.26.1",
			wantContent: "module test\n\ngo 1.26.0\n\ntoolchain go1.26.1\n",
			wantChanged: false,
		},
		{
			name:        "commented go directive is ignored",
			content:     "module test\n\n// go 1.23.0\n",
			version:     "1.26.1",
			wantContent: "module test\n\n// go 1.23.0\n",
			wantChanged: false,
		},
		{
			name:        "invalid version in go directive is ignored",
			content:     "module test\n\ngo 1.23.invalid\n",
			version:     "1.26.1",
			wantContent: "module test\n\ngo 1.23.invalid\n",
			wantChanged: false,
		},
		{
			name:        "no go directive",
			content:     "module test\n",
			version:     "1.26.1",
			wantContent: "module test\n",
			wantChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := bumpGoMod(t.Context(), tt.content, tt.version)
			if got != tt.wantContent {
				t.Errorf("bumpGoMod() got = %q, want %q", got, tt.wantContent)
			}
			if changed != tt.wantChanged {
				t.Errorf("bumpGoMod() changed = %v, want %v", changed, tt.wantChanged)
			}
		})
	}
}

func TestBumpDockerfile(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		version     string
		wantContent string
		wantChanged bool
	}{
		{
			name:        "with suffix",
			content:     "FROM golang:1.24-bookworm\n",
			version:     "1.26.1",
			wantContent: "FROM golang:1.26.1-bookworm\n",
			wantChanged: true,
		},
		{
			name:        "with platform flag",
			content:     "FROM --platform=$BUILDPLATFORM golang:1.25.1-trixie AS builder\n",
			version:     "1.26.1",
			wantContent: "FROM --platform=$BUILDPLATFORM golang:1.26.1-trixie AS builder\n",
			wantChanged: true,
		},
		{
			name:        "registry-qualified",
			content:     "FROM docker.io/library/golang:1.25-alpine\n",
			version:     "1.26.1",
			wantContent: "FROM docker.io/library/golang:1.26.1-alpine\n",
			wantChanged: true,
		},
		{
			name:        "with image digest",
			content:     "FROM golang:1.25.1-trixie@sha256:abcdef123456\n",
			version:     "1.26.1",
			wantContent: "FROM golang:1.25.1-trixie@sha256:abcdef123456\n",
			wantChanged: false,
		},
		{
			name:        "without suffix",
			content:     "FROM golang:1.24\n",
			version:     "1.26.1",
			wantContent: "FROM golang:1.26.1\n",
			wantChanged: true,
		},
		{
			name:        "comment mentioning golang is ignored",
			content:     "# see golang:1.20 notes\nFROM debian:bookworm\n",
			version:     "1.26.1",
			wantContent: "# see golang:1.20 notes\nFROM debian:bookworm\n",
			wantChanged: false,
		},
		{
			name:        "RUN instruction mentioning golang is ignored",
			content:     "RUN echo golang:1.26\n",
			version:     "1.26.1",
			wantContent: "RUN echo golang:1.26\n",
			wantChanged: false,
		},
		{
			name:        "already up to date",
			content:     "FROM golang:1.26.1-bookworm\n",
			version:     "1.26.1",
			wantContent: "FROM golang:1.26.1-bookworm\n",
			wantChanged: false,
		},
		{
			name:        "non-version tag like alpine",
			content:     "FROM golang:alpine\n",
			version:     "1.26.1",
			wantContent: "FROM golang:alpine\n",
			wantChanged: false,
		},
		{
			name:        "non-version tag like latest",
			content:     "FROM golang:latest\n",
			version:     "1.26.1",
			wantContent: "FROM golang:latest\n",
			wantChanged: false,
		},
		{
			name:        "unrelated image",
			content:     "FROM debian:bookworm\n",
			version:     "1.26.1",
			wantContent: "FROM debian:bookworm\n",
			wantChanged: false,
		},
		{
			name:        "empty content",
			content:     "",
			version:     "1.26.1",
			wantContent: "",
			wantChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := bumpDockerfile(t.Context(), tt.content, tt.version)
			if got != tt.wantContent {
				t.Errorf("bumpDockerfile() got = %q, want %q", got, tt.wantContent)
			}
			if changed != tt.wantChanged {
				t.Errorf("bumpDockerfile() changed = %v, want %v", changed, tt.wantChanged)
			}
		})
	}
}
