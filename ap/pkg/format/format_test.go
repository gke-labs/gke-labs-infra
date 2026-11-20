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

package format

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
)

func TestFormatTasks_NotExist(t *testing.T) {
	tmpDir := t.TempDir()
	r, err := sourcetree.Open(t.Context(), tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	task, err := FormatTasks(r, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error when dev/tasks does not exist: %v", err)
	}
	if task == nil {
		t.Fatal("expected non-nil task")
	}
}

func TestFormatTasks_WithTasks(t *testing.T) {
	tmpDir := t.TempDir()
	tasksDir := filepath.Join(tmpDir, "dev", "tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(tasksDir, "format-foo")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	r, err := sourcetree.Open(t.Context(), tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	task, err := FormatTasks(r, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task == nil {
		t.Fatal("expected non-nil task")
	}
}

func TestFormatTasks_NilRepo(t *testing.T) {
	tmpDir := t.TempDir()
	_, err := FormatTasks(nil, tmpDir)
	if err == nil {
		t.Fatal("expected error when repo is nil, got nil")
	}
}

func TestFormatTasks_UnreadableTasksDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}
	tmpDir := t.TempDir()
	devDir := filepath.Join(tmpDir, "dev")
	if err := os.MkdirAll(devDir, 0755); err != nil {
		t.Fatal(err)
	}
	tasksDir := filepath.Join(devDir, "tasks")
	if err := os.Mkdir(tasksDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tasksDir, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(tasksDir, 0755); err != nil {
			t.Logf("cleanup chmod failed: %v", err)
		}
	})

	_, err := sourcetree.Open(t.Context(), tmpDir)
	if err == nil {
		t.Fatal("expected error when dev/tasks is unreadable, got nil")
	}
}
