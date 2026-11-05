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
	"io"
	"os"
	"sync"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/kubectl"
)

var (
	ensureKubectl      = kubectl.EnsureKubectlForRepo
	kubectlResolveMu   sync.Mutex
	scriptEnvResolveMu sync.Mutex
)

// TaskContext represents the context for an ap root directory.
//
// A TaskContext always has a source tree by construction; access is through the
// SourceTree method, which is guaranteed never to return nil.
type TaskContext struct {
	repoRoot   string
	dir        string
	sourceTree *sourcetree.SourceTree

	stdout io.Writer
	stderr io.Writer

	// kubectlBin holds the resolved path to the kubectl binary for this task context.
	// If empty, EnsureKubectl resolves it lazily and memoizes it here.
	kubectlBin string

	// scriptEnv holds the memoized environment with the tool bin directory prepended to PATH.
	scriptEnv []string

	// Discovered Tasks
	BuildTasks    []Task
	E2ETasks      []Task
	TestTasks     []Task
	LintTasks     []Task
	GenerateTasks []Task
	FormatTasks   []Task
}

// NewTaskContext constructs a TaskContext for the given repoRoot, dir, and source tree.
// Every TaskContext must have a non-nil tree by construction; NewTaskContext panics if tree is nil.
func NewTaskContext(repoRoot, dir string, tree *sourcetree.SourceTree) *TaskContext {
	if tree == nil {
		panic("tasks.NewTaskContext: tree cannot be nil")
	}
	return &TaskContext{
		repoRoot:   repoRoot,
		dir:        dir,
		sourceTree: tree,
	}
}

// RepoRoot returns the repository root directory.
func (s *TaskContext) RepoRoot() string {
	return s.repoRoot
}

// Dir returns the target directory for this task context.
func (s *TaskContext) Dir() string {
	return s.dir
}

// SourceTree returns the source tree associated with this task context.
func (s *TaskContext) SourceTree() *sourcetree.SourceTree {
	return s.sourceTree
}

// Stdout returns the configured stdout writer, or os.Stdout if unset.
func (s *TaskContext) Stdout() io.Writer {
	if s.stdout != nil {
		return s.stdout
	}
	return os.Stdout
}

// Stderr returns the configured stderr writer, or os.Stderr if unset.
func (s *TaskContext) Stderr() io.Writer {
	if s.stderr != nil {
		return s.stderr
	}
	return os.Stderr
}

// WithOutput returns a shallow copy of the task context with custom stdout and stderr writers.
func (s *TaskContext) WithOutput(stdout, stderr io.Writer) *TaskContext {
	clone := *s
	clone.stdout = stdout
	clone.stderr = stderr
	return &clone
}

// EnsureKubectl returns the path to the pinned kubectl binary for this task context,
// resolving and memoizing it if not yet set.
func (s *TaskContext) EnsureKubectl(ctx context.Context) (string, error) {
	kubectlResolveMu.Lock()
	defer kubectlResolveMu.Unlock()
	if s.kubectlBin != "" {
		return s.kubectlBin, nil
	}
	root := s.repoRoot
	if root == "" {
		root = s.dir
	}
	bin, err := ensureKubectl(ctx, root)
	if err != nil {
		return "", err
	}
	s.kubectlBin = bin
	return bin, nil
}

// ScriptEnv returns an environment slice with the tool bin directory prepended to PATH,
// populated once per task context using tool.ScriptEnv and memoized.
func (s *TaskContext) ScriptEnv() ([]string, error) {
	scriptEnvResolveMu.Lock()
	defer scriptEnvResolveMu.Unlock()
	if s.scriptEnv != nil {
		return s.scriptEnv, nil
	}

	env, err := tool.ScriptEnv(s.repoRoot)
	if err != nil {
		return nil, err
	}
	s.scriptEnv = env
	return env, nil
}
