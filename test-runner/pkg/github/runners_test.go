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

package github

import "testing"

func TestParseScope(t *testing.T) {
	tests := []struct {
		name    string
		repo    string
		org     string
		want    Scope
		wantErr bool
	}{
		{name: "repo", repo: "gke-labs/gke-labs-infra", want: Scope{Owner: "gke-labs", Repo: "gke-labs-infra"}},
		{name: "org", org: "gke-labs", want: Scope{Owner: "gke-labs"}},
		{name: "both", repo: "a/b", org: "c", wantErr: true},
		{name: "neither", wantErr: true},
		{name: "repo without owner", repo: "gke-labs-infra", wantErr: true},
		{name: "repo with extra slash", repo: "a/b/c", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseScope(tt.repo, tt.org)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseScope(%q, %q) error = %v, wantErr %v", tt.repo, tt.org, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParseScope(%q, %q) = %+v, want %+v", tt.repo, tt.org, got, tt.want)
			}
		})
	}
}
