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

package commands

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gke-labs/gke-labs-infra/github-admin/pkg/config"
)

func TestLoadConfigs(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name    string
		content string
		want    []config.RepositoryConfig
		wantErr bool
	}{
		{
			name: "List format (Not supported anymore as per PR feedback)",
			content: `- owner: org1
  name: repo1
- owner: org2
  name: repo2
`,
			// It will try to parse list as single object -> fail?
			// Actually, mapstructure/yaml might partial match or fail.
			// Since we removed list support, this test case expectation should change or be removed.
			// If we parse a list as a struct, it usually errors because [] != struct.
			wantErr: true,
		},
		{
			name: "Multi-doc format",
			content: `owner: org1
name: repo1
---
owner: org2
name: repo2
`,
			want: []config.RepositoryConfig{
				{Owner: "org1", Name: "repo1"},
				{Owner: "org2", Name: "repo2"},
			},
		},
		{
			name:    "Invalid YAML",
			content: `invalid: [`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(tempDir, "config.yaml")
			if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
				t.Fatalf("failed to write config file: %v", err)
			}

			got, err := LoadConfigs(path)
			if (err != nil) != tt.wantErr {
				t.Errorf("LoadConfigs() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("LoadConfigs() = %v, want %v", got, tt.want)
			}
		})
	}
}
