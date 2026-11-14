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

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildKindLoadPlan(t *testing.T) {
	tests := []struct {
		name           string
		clusterFlag    string
		currentContext string
		images         []string
		wantCluster    string
		wantErr        string
	}{
		{
			name:        "kind scheme",
			clusterFlag: "kind://dev",
			images:      []string{"alpine:3.22", "nginx:1.27"},
			wantCluster: "dev",
		},
		{
			name:           "kind context from current context",
			currentContext: "kind-dev",
			images:         []string{"alpine:3.22"},
			wantCluster:    "dev",
		},
		{
			name:        "kind context flag",
			clusterFlag: "kind-dev",
			images:      []string{"alpine:3.22"},
			wantCluster: "dev",
		},
		{
			name:        "non-kind cluster flag error",
			clusterFlag: "prod-cluster",
			images:      []string{"alpine:3.22"},
			wantErr:     "prod-cluster is not a kind cluster; pass --cluster kind://<name>",
		},
		{
			name:           "non-kind current context error",
			currentContext: "my-context",
			images:         []string{"alpine:3.22"},
			wantErr:        "my-context is not a kind cluster; pass --cluster kind://<name>",
		},
		{
			name:    "empty cluster and context error",
			images:  []string{"alpine:3.22"},
			wantErr: "current context is not a kind cluster; pass --cluster kind://<name>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opt := KindLoadOptions{
				Cluster: tt.clusterFlag,
				Images:  tt.images,
			}
			task, err := BuildKindLoadPlan(opt, tt.currentContext)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("BuildKindLoadPlan() error = nil, want %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("BuildKindLoadPlan() error = %q, want containing %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("BuildKindLoadPlan() error = %v, want nil", err)
			}
			if task.ClusterName != tt.wantCluster {
				t.Errorf("ClusterName = %q, want %q", task.ClusterName, tt.wantCluster)
			}
			if len(task.Images) != len(tt.images) {
				t.Fatalf("len(task.Images) = %d, want %d", len(task.Images), len(tt.images))
			}
			for i, img := range tt.images {
				if got := task.Images[i].String(); got != img {
					t.Errorf("task.Images[%d] = %q, want %q", i, got, img)
				}
			}
		})
	}
}

func TestRunKindLoad_DryRun(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	opt := KindLoadOptions{
		RootOptions: &RootOptions{RepoRoot: tempDir, Dir: tempDir, DryRun: true},
		Cluster:     "kind://test-cluster",
		Images:      []string{"alpine:3.22"},
	}

	if err := RunKindLoad(t.Context(), opt); err != nil {
		t.Fatalf("RunKindLoad dry-run failed: %v", err)
	}
}
