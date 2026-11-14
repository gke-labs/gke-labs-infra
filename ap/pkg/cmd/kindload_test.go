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
	"testing"
)

func TestResolveKindClusterName(t *testing.T) {
	tests := []struct {
		name, clusterFlag, currentContext, want string
		wantErr                                 bool
	}{
		{name: "kind scheme", clusterFlag: "kind://dev", want: "dev"},
		{name: "kind context prefix", clusterFlag: "kind-dev", want: "dev"},
		{name: "plain cluster name", clusterFlag: "dev", want: "dev"},
		{name: "empty flag with kind context", currentContext: "kind-prod", want: "prod"},
		{name: "empty flag with non-kind context", currentContext: "my-context", want: ""},
		{name: "empty flag and empty context", want: ""},
		{name: "invalid scheme", clusterFlag: "docker://dev", wantErr: true},
		{name: "empty kind scheme", clusterFlag: "kind://", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveKindClusterName(tt.clusterFlag, tt.currentContext)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveKindClusterName(%q, %q) error = %v, wantErr %v", tt.clusterFlag, tt.currentContext, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("resolveKindClusterName(%q, %q) = %q, want %q", tt.clusterFlag, tt.currentContext, got, tt.want)
			}
		})
	}
}

func TestBuildKindLoadPlan(t *testing.T) {
	t.Run("valid images and cluster", func(t *testing.T) {
		opt := KindLoadOptions{
			Cluster: "kind://test-cluster",
			Images:  []string{"alpine:3.22", "nginx:1.27"},
		}
		task, err := BuildKindLoadPlan(opt, "")
		if err != nil {
			t.Fatalf("BuildKindLoadPlan failed: %v", err)
		}
		if task.ClusterName != "test-cluster" {
			t.Errorf("ClusterName = %q, want 'test-cluster'", task.ClusterName)
		}
		if len(task.Images) != 2 || task.Images[0].String() != "alpine:3.22" || task.Images[1].String() != "nginx:1.27" {
			t.Errorf("Images = %v, want [alpine:3.22 nginx:1.27]", task.Images)
		}
		if got := task.GetName(); got != "kind-load (cluster: test-cluster): 2 images" {
			t.Errorf("GetName() = %q, want 'kind-load (cluster: test-cluster): 2 images'", got)
		}
	})

	t.Run("empty images", func(t *testing.T) {
		opt := KindLoadOptions{Cluster: "kind://test-cluster"}
		if _, err := BuildKindLoadPlan(opt, ""); err == nil {
			t.Fatal("expected error for empty images, got nil")
		}
	})

	t.Run("invalid image format", func(t *testing.T) {
		opt := KindLoadOptions{Cluster: "kind://test-cluster", Images: []string{""}}
		if _, err := BuildKindLoadPlan(opt, ""); err == nil {
			t.Fatal("expected error for invalid image, got nil")
		}
	})
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
