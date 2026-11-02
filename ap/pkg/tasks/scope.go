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

// APScope represents the context for an ap root directory.
//
// A scope always has a source tree by construction; access is through the
// SourceTree field directly, which is guaranteed never to be nil.
type APScope struct {
	RepoRoot   string
	Dir        string
	SourceTree *sourcetree.SourceTree

	Stdout io.Writer
	Stderr io.Writer

	// KubectlBin holds the resolved path to the kubectl binary for this scope.
	// If empty, EnsureKubectl resolves it lazily and memoizes it here.
	KubectlBin string

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

// NewScope constructs an APScope for the given repoRoot, dir, and source tree.
// Every scope must have a non-nil tree by construction; NewScope panics if tree is nil.
func NewScope(repoRoot, dir string, tree *sourcetree.SourceTree) *APScope {
	if tree == nil {
		panic("tasks.NewScope: tree cannot be nil")
	}
	return &APScope{
		RepoRoot:   repoRoot,
		Dir:        dir,
		SourceTree: tree,
	}
}

// GetStdout returns the configured stdout writer, or os.Stdout if unset.
func (s *APScope) GetStdout() io.Writer {
	if s.Stdout != nil {
		return s.Stdout
	}
	return os.Stdout
}

// GetStderr returns the configured stderr writer, or os.Stderr if unset.
func (s *APScope) GetStderr() io.Writer {
	if s.Stderr != nil {
		return s.Stderr
	}
	return os.Stderr
}

// WithOutput returns a shallow copy of the scope with custom stdout and stderr writers.
func (s *APScope) WithOutput(stdout, stderr io.Writer) *APScope {
	clone := *s
	clone.Stdout = stdout
	clone.Stderr = stderr
	return &clone
}

// EnsureKubectl returns the path to the pinned kubectl binary for this scope,
// resolving and memoizing it if not yet set.
func (s *APScope) EnsureKubectl(ctx context.Context) (string, error) {
	kubectlResolveMu.Lock()
	defer kubectlResolveMu.Unlock()
	if s.KubectlBin != "" {
		return s.KubectlBin, nil
	}
	root := s.RepoRoot
	if root == "" {
		root = s.Dir
	}
	bin, err := ensureKubectl(ctx, root)
	if err != nil {
		return "", err
	}
	s.KubectlBin = bin
	return bin, nil
}

// ScriptEnv returns an environment slice with the tool bin directory prepended to PATH,
// populated once per scope using tool.ScriptEnv and memoized.
func (s *APScope) ScriptEnv() ([]string, error) {
	scriptEnvResolveMu.Lock()
	defer scriptEnvResolveMu.Unlock()
	if s.scriptEnv != nil {
		return s.scriptEnv, nil
	}

	env, err := tool.ScriptEnv(s.RepoRoot)
	if err != nil {
		return nil, err
	}
	s.scriptEnv = env
	return env, nil
}
