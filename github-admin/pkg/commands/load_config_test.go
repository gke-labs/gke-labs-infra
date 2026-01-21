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
			name: "List format",
			content: `- owner: org1
  name: repo1
- owner: org2
  name: repo2
`,
			want: []config.RepositoryConfig{
				{Owner: "org1", Name: "repo1"},
				{Owner: "org2", Name: "repo2"},
			},
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
			name: "Multi-doc with list (mixed - unlikely but possible)",
			content: `- owner: org1
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
