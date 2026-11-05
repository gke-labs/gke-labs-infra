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

package kindload

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/imageref"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

func TestKindLoadTask_GetName(t *testing.T) {
	refA := &imageref.ImageRef{Prefix: "images.local", Name: "a", Tag: "v1.0"}
	refB := &imageref.ImageRef{Prefix: "images.local", Name: "b", Tag: "v1.0"}

	tests := []struct {
		name        string
		clusterName string
		images      []*imageref.ImageRef
		want        string
	}{
		{
			name:        "empty images without cluster",
			clusterName: "",
			images:      nil,
			want:        "kind-load: 0 images",
		},
		{
			name:        "empty images with cluster",
			clusterName: "kind-ci",
			images:      nil,
			want:        "kind-load (cluster: kind-ci): 0 images",
		},
		{
			name:        "one image with cluster",
			clusterName: "test-cluster",
			images:      []*imageref.ImageRef{refA},
			want:        "kind-load (cluster: test-cluster): 1 images",
		},
		{
			name:        "multiple images with cluster",
			clusterName: "test-cluster",
			images:      []*imageref.ImageRef{refA, refB},
			want:        "kind-load (cluster: test-cluster): 2 images",
		},
		{
			name:        "multiple images without cluster",
			clusterName: "",
			images:      []*imageref.ImageRef{refA, refB},
			want:        "kind-load: 2 images",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &KindLoadTask{
				ClusterName: tt.clusterName,
				Images:      tt.images,
			}
			if got := task.GetName(); got != tt.want {
				t.Errorf("task.GetName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestKindLoadTask_GetChildren(t *testing.T) {
	refA := &imageref.ImageRef{Prefix: "images.local", Name: "a", Tag: "v1.0"}
	refB := &imageref.ImageRef{Prefix: "images.local", Name: "b", Tag: "v1.0"}

	task := &KindLoadTask{
		ClusterName: "test-cluster",
		Images:      []*imageref.ImageRef{refA, refB},
	}

	children := task.GetChildren()
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}
	if children[0].GetName() != "images.local/a:v1.0" {
		t.Errorf("child[0].GetName() = %q, want %q", children[0].GetName(), "images.local/a:v1.0")
	}
	if children[1].GetName() != "images.local/b:v1.0" {
		t.Errorf("child[1].GetName() = %q, want %q", children[1].GetName(), "images.local/b:v1.0")
	}

	// Empty images returns nil children
	emptyTask := &KindLoadTask{
		ClusterName: "test-cluster",
		Images:      nil,
	}
	if emptyTask.GetChildren() != nil {
		t.Errorf("expected nil children for empty task, got: %v", emptyTask.GetChildren())
	}
}

func TestKindLoadTask_SharedReferenceMutation(t *testing.T) {
	// Construct the real shared reference as built at plan time
	ref := &imageref.ImageRef{
		Prefix: "images.local",
		Name:   "my-app",
		Tag:    "latest",
	}

	task := &KindLoadTask{
		ClusterName: "test-cluster",
		Images:      []*imageref.ImageRef{ref},
	}

	// At plan time, base reference is inspected
	if got := task.GetChildren()[0].GetName(); got != "images.local/my-app:latest" {
		t.Fatalf("at plan time: child = %q, want %q", got, "images.local/my-app:latest")
	}

	// Simulate build execution mutating the shared reference with unique content tag
	ref.Tag = "latest-abcdef123456"

	// Task reads the same reference and carries the build-produced tag
	if got := task.GetChildren()[0].GetName(); got != "images.local/my-app:latest-abcdef123456" {
		t.Fatalf("after build: child = %q, want %q", got, "images.local/my-app:latest-abcdef123456")
	}
}

func TestKindLoadTask_Run(t *testing.T) {
	t.Run("empty images is a no-op", func(t *testing.T) {
		tempDir := t.TempDir()
		tree, err := sourcetree.Open(tempDir)
		if err != nil {
			t.Fatal(err)
		}
		scope := tasks.NewTaskContext(tempDir, tempDir, tree)
		task := &KindLoadTask{
			ClusterName: "test-cluster",
			Images:      nil,
		}
		if err := task.Run(t.Context(), scope); err != nil {
			t.Fatalf("expected nil for empty images, got: %v", err)
		}
	})

	t.Run("executes kind load with image refs", func(t *testing.T) {
		tempDir := t.TempDir()
		argsLog := filepath.Join(tempDir, "args.log")
		fakeBin := filepath.Join(tempDir, "kind")

		script := fmt.Sprintf(`#!/bin/sh
echo "$@" > %q
exit 0
`, argsLog)

		if err := os.WriteFile(fakeBin, []byte(script), 0755); err != nil {
			t.Fatal(err)
		}

		origEnsureKind := ensureKind
		ensureKind = func(ctx context.Context, repoRoot string) (string, error) {
			return fakeBin, nil
		}
		t.Cleanup(func() {
			ensureKind = origEnsureKind
		})

		ref1 := &imageref.ImageRef{Prefix: "images.local", Name: "app1", Tag: "v1.0"}
		ref2 := &imageref.ImageRef{Prefix: "images.local", Name: "app2", Tag: "latest-1234567890ab"}

		task := &KindLoadTask{
			RepoRoot:    tempDir,
			ClusterName: "demo-cluster",
			Images:      []*imageref.ImageRef{ref1, ref2},
		}

		tree, err := sourcetree.Open(tempDir)
		if err != nil {
			t.Fatal(err)
		}
		scope := tasks.NewTaskContext(tempDir, tempDir, tree)

		if err := task.Run(t.Context(), scope); err != nil {
			t.Fatalf("task.Run failed: %v", err)
		}

		content, err := os.ReadFile(argsLog)
		if err != nil {
			t.Fatalf("failed to read args log: %v", err)
		}

		gotArgs := strings.Fields(string(content))
		expectedArgs := []string{
			"load", "docker-image",
			"images.local/app1:v1.0",
			"images.local/app2:latest-1234567890ab",
			"--name", "demo-cluster",
		}

		if !reflect.DeepEqual(gotArgs, expectedArgs) {
			t.Errorf("args = %v, want %v", gotArgs, expectedArgs)
		}
	})
}

func TestKindLoadTask_RetryPolicy(t *testing.T) {
	task := &KindLoadTask{}
	policy := task.RetryPolicy()
	if !reflect.DeepEqual(policy, tasks.Backoff(DefaultBackoffs)) {
		t.Errorf("task.RetryPolicy() = %v, want %v", policy, tasks.Backoff(DefaultBackoffs))
	}
}
