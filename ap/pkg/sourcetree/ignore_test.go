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

package sourcetree

import (
	"strings"
	"testing"
)

func TestNewIgnoreList(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		wantErr  string
	}{
		{
			name:     "valid list builds",
			patterns: []string{"*.go", "vendor/", "foo/**/bar"},
			wantErr:  "",
		},
		{
			name:     "malformed glob returns error naming it",
			patterns: []string{"["},
			wantErr:  "[",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			list, err := NewIgnoreList(tc.patterns)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if list == nil {
					t.Fatal("expected non-nil IgnoreList")
				}
			} else {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
			}
		})
	}
}
