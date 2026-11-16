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
	"fmt"
	"io"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
)

// Task is the interface that all tasks must implement.
type Task interface {
	Run(ctx context.Context, scope *TaskContext) error
	GetName() string
	GetChildren() []Task
}

// FuncTask represents a task that executes a function.
type FuncTask struct {
	Name  string
	RunFn func(ctx context.Context, scope *TaskContext) error
}

func (t *FuncTask) Run(ctx context.Context, scope *TaskContext) error {
	return t.RunFn(ctx, scope)
}

func (t *FuncTask) GetName() string {
	return t.Name
}

func (t *FuncTask) GetChildren() []Task {
	return nil
}

// PrintTree prints the task tree to the specified writer.
func PrintTree(w io.Writer, t Task, indent int) {
	if _, err := fmt.Fprintf(w, "%s%s\n", strings.Repeat("  ", indent), t.GetName()); err != nil {
		scoped.Log(context.Background()).V(2).Info("failed to write task tree", "error", err)
	}
	for _, child := range t.GetChildren() {
		PrintTree(w, child, indent+1)
	}
}
