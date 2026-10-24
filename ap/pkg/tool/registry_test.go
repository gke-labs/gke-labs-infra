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

package tool

import (
	"testing"
)

func TestResolveTool(t *testing.T) {
	tests := []struct {
		argv0       string
		wantName    string
		wantResolve bool
	}{
		// Registered tool names
		{argv0: "kind", wantName: "kind", wantResolve: true},
		{argv0: "docker", wantName: "docker", wantResolve: true},
		{argv0: "docker-buildx", wantName: "docker-buildx", wantResolve: true},
		{argv0: "protoc", wantName: "protoc", wantResolve: true},

		// Executable paths with directories
		{argv0: "/usr/local/bin/kind", wantName: "kind", wantResolve: true},
		{argv0: "/usr/bin/docker", wantName: "docker", wantResolve: true},
		{argv0: "/opt/bin/docker-buildx", wantName: "docker-buildx", wantResolve: true},
		{argv0: "./bin/protoc", wantName: "protoc", wantResolve: true},

		// Windows .exe extensions
		{argv0: "kind.exe", wantName: "kind", wantResolve: true},
		{argv0: "docker.exe", wantName: "docker", wantResolve: true},
		{argv0: "docker-buildx.exe", wantName: "docker-buildx", wantResolve: true},
		{argv0: "protoc.exe", wantName: "protoc", wantResolve: true},
		{argv0: "C:\\tools\\bin\\kind.exe", wantName: "kind", wantResolve: true},
		{argv0: "C:\\tools\\bin\\docker.exe", wantName: "docker", wantResolve: true},

		// Unknown / normal CLI names that fall through
		{argv0: "ap", wantResolve: false},
		{argv0: "ap.exe", wantResolve: false},
		{argv0: "/usr/local/bin/ap", wantResolve: false},
		{argv0: "other", wantResolve: false},
		{argv0: "", wantResolve: false},
	}

	for _, tc := range tests {
		t.Run(tc.argv0, func(t *testing.T) {
			gotTool, gotResolve := Resolve(tc.argv0)
			if gotResolve != tc.wantResolve {
				t.Fatalf("Resolve(%q) got resolve=%v, want %v", tc.argv0, gotResolve, tc.wantResolve)
			}
			if tc.wantResolve {
				if gotTool == nil {
					t.Fatalf("Resolve(%q) returned nil tool", tc.argv0)
				}
				if gotTool.Name != tc.wantName {
					t.Fatalf("Resolve(%q) tool name = %q, want %q", tc.argv0, gotTool.Name, tc.wantName)
				}
			} else {
				if gotTool != nil {
					t.Fatalf("Resolve(%q) expected nil tool, got %v", tc.argv0, gotTool)
				}
			}
		})
	}
}
