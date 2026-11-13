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
		t.Setenv("PATH", tempDir+":"+os.Getenv("PATH"))
		argsLog := filepath.Join(tempDir, "args.log")
		dockerLog := filepath.Join(tempDir, "docker.log")
		fakeBin := filepath.Join(tempDir, "kind")
		fakeDocker := filepath.Join(tempDir, "docker")

		script := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %q
if [ "$1" = "get" ] && [ "$2" = "nodes" ]; then
    echo "demo-node"
    exit 0
fi
exit 0
`, argsLog)

		if err := os.WriteFile(fakeBin, []byte(script), 0755); err != nil {
			t.Fatal(err)
		}

		dockerScript := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %q
if [ "$1" = "save" ]; then
    touch "$3"
    exit 0
fi
if [ "$1" = "exec" ]; then
    exit 0
fi
exit 1
`, dockerLog)

		if err := os.WriteFile(fakeDocker, []byte(dockerScript), 0755); err != nil {
			t.Fatal(err)
		}

		ref1 := &imageref.ImageRef{Prefix: "images.local", Name: "app1", Tag: "v1.0"}
		ref2 := &imageref.ImageRef{Prefix: "images.local", Name: "app2", Tag: "latest-1234567890ab"}

		task := &KindLoadTask{
			ClusterName: "demo-cluster",
			Images:      []*imageref.ImageRef{ref1, ref2},
		}

		tree, err := sourcetree.Open(tempDir)
		if err != nil {
			t.Fatal(err)
		}
		scope := tasks.NewTaskContext(tempDir, tempDir, tree).WithTool("kind", fakeBin)

		if err := task.Run(t.Context(), scope); err != nil {
			t.Fatalf("task.Run failed: %v", err)
		}

		content, err := os.ReadFile(argsLog)
		if err != nil {
			t.Fatalf("failed to read args log: %v", err)
		}

		gotArgs := strings.Fields(string(content))
		expectedArgs := []string{
			"get", "nodes",
			"--name", "demo-cluster",
		}

		if !reflect.DeepEqual(gotArgs, expectedArgs) {
			t.Errorf("args = %v, want %v", gotArgs, expectedArgs)
		}

		dockerContent, err := os.ReadFile(dockerLog)
		if err != nil {
			t.Fatalf("failed to read docker log: %v", err)
		}
		dockerLines := strings.Split(strings.TrimSpace(string(dockerContent)), "\n")
		if len(dockerLines) != 2 {
			t.Fatalf("expected 2 docker commands logged, got %d:\n%s", len(dockerLines), string(dockerContent))
		}
		if !strings.HasPrefix(dockerLines[0], "save -o ") || !strings.Contains(dockerLines[0], "images.local/app1:v1.0 images.local/app2:latest-1234567890ab") {
			t.Errorf("expected docker save with both images, got: %q", dockerLines[0])
		}
		if !strings.Contains(dockerLines[1], "exec --privileged -i demo-node ctr --namespace=k8s.io images import --digests -") {
			t.Errorf("expected docker exec ctr import into demo-node, got: %q", dockerLines[1])
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
