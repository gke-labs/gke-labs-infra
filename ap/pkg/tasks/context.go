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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool"
)

type runState struct {
	mutex     sync.Mutex
	usedPaths map[string]bool
}

var (
	toolResolveMu      sync.Mutex
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

	stdout        io.Writer
	stderr        io.Writer
	printOutput   bool
	verify        bool
	writeBaseline bool
	race          bool
	artifactsDir  string
	taskDir       string
	taskLogFile   string
	runState      *runState
	parents       []string

	// tools holds memoized resolved tool binary paths keyed by tool name.
	tools map[string]string

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
		repoRoot:     repoRoot,
		dir:          dir,
		sourceTree:   tree,
		tools:        make(map[string]string),
		artifactsDir: os.Getenv("ARTIFACTS"),
		runState:     &runState{usedPaths: make(map[string]bool)},
	}
}

func (s *TaskContext) withParent(name string) *TaskContext {
	clone := *s
	clone.parents = append(append([]string(nil), s.parents...), name)
	return &clone
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

// PrintOutput returns whether leaf tasks should print their buffered output even on success.
func (s *TaskContext) PrintOutput() bool {
	return s.printOutput
}

// WithPrintOutput returns a shallow copy of the task context with the given printOutput setting.
func (s *TaskContext) WithPrintOutput(printOutput bool) *TaskContext {
	clone := *s
	clone.printOutput = printOutput
	return &clone
}

// Verify returns whether tasks should execute in verification mode.
func (s *TaskContext) Verify() bool {
	return s.verify
}

// WithVerify returns a shallow copy of the task context with the given verify setting.
func (s *TaskContext) WithVerify(verify bool) *TaskContext {
	clone := *s
	clone.verify = verify
	return &clone
}

// WriteBaseline returns whether tasks should write baseline files.
func (s *TaskContext) WriteBaseline() bool {
	return s.writeBaseline
}

// WithWriteBaseline returns a shallow copy of the task context with the given writeBaseline setting.
func (s *TaskContext) WithWriteBaseline(writeBaseline bool) *TaskContext {
	clone := *s
	clone.writeBaseline = writeBaseline
	return &clone
}

// Race returns whether tests should run under the data race detector.
func (s *TaskContext) Race() bool {
	return s.race
}

// WithRace returns a shallow copy of the task context with the given race setting.
func (s *TaskContext) WithRace(race bool) *TaskContext {
	clone := *s
	clone.race = race
	return &clone
}

// ArtifactsDir returns the directory where task artifacts should be written.
func (s *TaskContext) ArtifactsDir() string {
	return s.artifactsDir
}

// WithArtifactsDir returns a shallow copy of the task context with custom artifacts directory.
func (s *TaskContext) WithArtifactsDir(dir string) *TaskContext {
	clone := *s
	clone.artifactsDir = dir
	return &clone
}

// WithTool returns a shallow copy of the task context with the given tool name pre-seeded to binPath.
func (s *TaskContext) WithTool(name, binPath string) *TaskContext {
	clone := *s
	clone.tools = make(map[string]string, len(s.tools)+1)
	for k, v := range s.tools {
		clone.tools[k] = v
	}
	clone.tools[name] = binPath
	return &clone
}

// Tool returns the path to the executable binary for the named tool,
// resolving it from the tool registry and memoizing it if not yet set.
func (s *TaskContext) Tool(ctx context.Context, name string) (string, error) {
	toolResolveMu.Lock()
	defer toolResolveMu.Unlock()
	if bin, ok := s.tools[name]; ok {
		return bin, nil
	}
	t, ok := tool.Lookup(name)
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	root := s.repoRoot
	if root == "" {
		root = s.dir
	}
	bin, err := t.Ensure(ctx, root)
	if err != nil {
		return "", err
	}
	s.tools[name] = bin
	return bin, nil
}

// ScriptEnv returns an environment slice with the tool bin directory prepended to PATH,
// populated once per task context using tool.ScriptEnv and memoized.
func (s *TaskContext) ScriptEnv(ctx context.Context) ([]string, error) {
	scriptEnvResolveMu.Lock()
	defer scriptEnvResolveMu.Unlock()
	if s.scriptEnv != nil {
		return s.scriptEnv, nil
	}

	env, err := tool.ScriptEnv(ctx, s.repoRoot)
	if err != nil {
		return nil, err
	}
	s.scriptEnv = env
	return env, nil
}

// Command holds the configuration for a command to execute.
type Command struct {
	Program               string
	Args                  []string
	Dir                   string
	Env                   []string
	MachineReadableStdout bool
}

// CommandResult holds the captured artifact paths from a command execution.
type CommandResult struct {
	StdoutFile string
	StderrFile string
}

// RunCommand executes a command, capturing stdout and stderr to files and writing
// both to the task log in stdout-then-stderr order upon completion (or streaming in e2e mode).
func (s *TaskContext) RunCommand(ctx context.Context, cmd Command) (*CommandResult, error) {
	if s.taskDir == "" {
		return nil, errors.New("task directory is not set on TaskContext")
	}

	stdoutFile, err := os.CreateTemp(s.taskDir, "cmd-*-stdout.log")
	if err != nil {
		return nil, fmt.Errorf("creating stdout capture file: %w", err)
	}

	stderrFile, err := os.CreateTemp(s.taskDir, "cmd-*-stderr.log")
	if err != nil {
		if closeErr := stdoutFile.Close(); closeErr != nil {
			return nil, fmt.Errorf("creating stderr capture file: %w (failed closing stdout: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("creating stderr capture file: %w", err)
	}

	var stdoutDestination io.Writer = stdoutFile
	var stderrDestination io.Writer = stderrFile

	if s.PrintOutput() {
		if !cmd.MachineReadableStdout {
			stdoutDestination = io.MultiWriter(stdoutFile, s.Stdout())
		}
		stderrDestination = io.MultiWriter(stderrFile, s.Stderr())
	}

	execCommand := exec.CommandContext(ctx, cmd.Program, cmd.Args...)
	execCommand.Dir = cmd.Dir
	if len(cmd.Env) > 0 {
		execCommand.Env = append(os.Environ(), cmd.Env...)
	}
	execCommand.Stdout = stdoutDestination
	execCommand.Stderr = stderrDestination

	execErr := execCommand.Run()
	if closeErr := stdoutFile.Close(); closeErr != nil {
		return nil, fmt.Errorf("closing stdout capture file: %w", closeErr)
	}
	if closeErr := stderrFile.Close(); closeErr != nil {
		return nil, fmt.Errorf("closing stderr capture file: %w", closeErr)
	}

	if !s.PrintOutput() {
		if !cmd.MachineReadableStdout {
			stdoutBytes, err := os.ReadFile(stdoutFile.Name())
			if err != nil {
				return nil, fmt.Errorf("reading stdout capture file: %w", err)
			}
			if len(stdoutBytes) > 0 {
				writef(s.Stdout(), "--- stdout ---\n%s", stdoutBytes)
				if !bytes.HasSuffix(stdoutBytes, []byte("\n")) {
					writef(s.Stdout(), "\n")
				}
			}
		}
		stderrBytes, err := os.ReadFile(stderrFile.Name())
		if err != nil {
			return nil, fmt.Errorf("reading stderr capture file: %w", err)
		}
		if len(stderrBytes) > 0 {
			writef(s.Stderr(), "--- stderr ---\n%s", stderrBytes)
			if !bytes.HasSuffix(stderrBytes, []byte("\n")) {
				writef(s.Stderr(), "\n")
			}
		}
	}

	result := &CommandResult{
		StdoutFile: stdoutFile.Name(),
		StderrFile: stderrFile.Name(),
	}
	if execErr != nil {
		commandName := cmd.Program
		if len(cmd.Args) > 0 {
			commandName = fmt.Sprintf("%s %s", cmd.Program, strings.Join(cmd.Args, " "))
		}
		return result, fmt.Errorf("%s: %w", commandName, execErr)
	}
	return result, nil
}
