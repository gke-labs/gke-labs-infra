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

package tasks

import (
	"context"
	"testing"
)

type fakeSourcedTask struct {
	name   string
	source string
}

func (t *fakeSourcedTask) Run(ctx context.Context, scope *TaskContext) error {
	return nil
}

func (t *fakeSourcedTask) GetName() string {
	return t.name
}

func (t *fakeSourcedTask) GetChildren() []Task {
	return nil
}

func (t *fakeSourcedTask) SourcePath() string {
	return t.source
}

var _ Sourced = (*fakeSourcedTask)(nil)

func TestFilterBySourcePath(t *testing.T) {
	taskA := &fakeSourcedTask{name: "task-a", source: "/repo/images/a/Dockerfile"}
	taskB := &fakeSourcedTask{name: "task-b", source: "/repo/images/b/Dockerfile"}
	taskC := &fakeSourcedTask{name: "task-c", source: "/repo/sub/dev/tasks/build-tool"}

	group := &Group{
		Name:  "all",
		Tasks: []Task{taskA, taskB, taskC},
	}

	t.Run("filter exact directory match", func(t *testing.T) {
		filtered := FilterBySourcePath([]Task{group}, []string{"/repo/images/a"})
		if len(filtered) != 1 {
			t.Fatalf("expected 1 task in filtered result, got %d", len(filtered))
		}
		g, ok := filtered[0].(*Group)
		if !ok {
			t.Fatalf("expected Group, got %T", filtered[0])
		}
		if len(g.Tasks) != 1 || g.Tasks[0].GetName() != "task-a" {
			t.Fatalf("expected task-a in filtered group, got %v", g.Tasks)
		}
	})

	t.Run("filter exact file match", func(t *testing.T) {
		filtered := FilterBySourcePath([]Task{group}, []string{"/repo/images/b/Dockerfile"})
		if len(filtered) != 1 {
			t.Fatalf("expected 1 task in filtered result, got %d", len(filtered))
		}
		g := filtered[0].(*Group)
		if len(g.Tasks) != 1 || g.Tasks[0].GetName() != "task-b" {
			t.Fatalf("expected task-b in filtered group, got %v", g.Tasks)
		}
	})

	t.Run("filter union of targets", func(t *testing.T) {
		filtered := FilterBySourcePath([]Task{group}, []string{"/repo/images/a", "/repo/sub"})
		if len(filtered) != 1 {
			t.Fatalf("expected 1 task in filtered result, got %d", len(filtered))
		}
		g := filtered[0].(*Group)
		if len(g.Tasks) != 2 {
			t.Fatalf("expected 2 tasks in filtered group, got %d", len(g.Tasks))
		}
		if g.Tasks[0].GetName() != "task-a" || g.Tasks[1].GetName() != "task-c" {
			t.Fatalf("expected task-a and task-c, got %v", g.Tasks)
		}
	})

	t.Run("filter with no matches drops group", func(t *testing.T) {
		filtered := FilterBySourcePath([]Task{group}, []string{"/repo/not-found"})
		if len(filtered) != 0 {
			t.Fatalf("expected 0 tasks, got %d", len(filtered))
		}
	})

	t.Run("retryable task wrapper preserves sourced and parent", func(t *testing.T) {
		retryable := &RetryableTask{
			Task: taskA,
		}
		if s, ok := Task(retryable).(Sourced); !ok || s.SourcePath() != "/repo/images/a/Dockerfile" {
			t.Errorf("expected RetryableTask to be Sourced with source path, got %v", s)
		}

		filtered := FilterBySourcePath([]Task{retryable}, []string{"/repo/images/a"})
		if len(filtered) != 1 || filtered[0].GetName() != "task-a" {
			t.Fatalf("expected retryable task to match, got %v", filtered)
		}
	})
}
