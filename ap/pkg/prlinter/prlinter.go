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

package prlinter

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"k8s.io/klog/v2"
)

// PRLintTask represents a task to run PR-specific linting checks.
type PRLintTask struct {
	RepoRoot string
}

func (t *PRLintTask) Run(ctx context.Context, scope *tasks.TaskContext) error {
	baseBranch, err := detectBaseBranch(ctx, t.RepoRoot)
	if err != nil {
		klog.V(2).Infof("Could not detect base branch: %v", err)
		return nil
	}

	if baseBranch == "" {
		klog.V(2).Info("No base branch detected, skipping PR lint")
		return nil
	}

	klog.Infof("Comparing against base branch %q", baseBranch)

	diff, err := getDiff(ctx, t.RepoRoot, baseBranch)
	if err != nil {
		return fmt.Errorf("error getting diff: %w", err)
	}

	if err := checkDoubleSpacing(diff); err != nil {
		return err
	}

	return nil
}

func (t *PRLintTask) GetName() string {
	return "pr-linter"
}

func (t *PRLintTask) GetChildren() []tasks.Task {
	return nil
}

// LintTasks returns a task for PR-specific linting.
func LintTasks(repoRoot string) (tasks.Task, error) {
	return &PRLintTask{RepoRoot: repoRoot}, nil
}

// Lint runs PR-specific linting checks.
func Lint(ctx context.Context, repoRoot string) error {
	t, err := LintTasks(repoRoot)
	if err != nil {
		return err
	}
	// prlinter is an independent entry point without an existing command scope;
	// open a sourcetree once for the repo root.
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		return err
	}
	return t.Run(ctx, tasks.NewTaskContext(repoRoot, repoRoot, tree))
}

func detectBaseBranch(ctx context.Context, repoRoot string) (string, error) {
	// git log -n 30 --format=%D
	cmd := exec.CommandContext(ctx, "git", "log", "-n", "30", "--format=%D")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}

	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		// line contains things like "HEAD -> branch, upstream/main, main"
		parts := strings.Split(line, ",")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "main" || part == "master" || strings.HasPrefix(part, "release-") {
				return part, nil
			}
			if strings.HasPrefix(part, "upstream/") || strings.HasPrefix(part, "origin/") {
				remoteBranch := strings.SplitN(part, "/", 2)[1]
				if remoteBranch == "main" || remoteBranch == "master" || strings.HasPrefix(remoteBranch, "release-") {
					return part, nil
				}
			}
		}
	}

	return "", nil
}

func getDiff(ctx context.Context, repoRoot, baseBranch string) (string, error) {
	// Find the merge base between baseBranch and HEAD
	cmd := exec.CommandContext(ctx, "git", "merge-base", baseBranch, "HEAD")
	cmd.Dir = repoRoot
	mergeBaseOut, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("error finding merge base: %w", err)
	}
	mergeBase := strings.TrimSpace(string(mergeBaseOut))

	// git diff mergeBase
	// This compares the merge base with the working tree, including staged changes.
	cmd = exec.CommandContext(ctx, "git", "diff", mergeBase)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func checkDoubleSpacing(diff string) error {
	lines := strings.Split(diff, "\n")

	var currentFile string
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "+++ b/") {
			currentFile = line[6:]
			continue
		}

		// Check alternating blank lines in a window
		if err := checkAlternatingAt(lines, i, currentFile); err != nil {
			return err
		}

		// Check error double spacing
		if err := checkErrorDoubleSpacingAt(lines, i, currentFile); err != nil {
			return err
		}
	}

	return nil
}

func checkAlternatingAt(lines []string, start int, filename string) error {
	const threshold = 8
	if start+threshold > len(lines) {
		return nil
	}

	count := 0
	expectBlank := false
	for i := start; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- ") || !strings.HasPrefix(line, "+") {
			break
		}

		content := line[1:]
		isBlank := strings.TrimSpace(content) == ""

		if count == 0 {
			if !isBlank {
				count = 1
				expectBlank = true
			} else {
				// Don't start with a blank line for this heuristic
				break
			}
		} else {
			if isBlank == expectBlank {
				count++
				expectBlank = !expectBlank
			} else {
				break
			}
		}

		if count >= threshold {
			return fmt.Errorf("detected double-spaced code in %s (8+ alternating blank lines)", filename)
		}
	}
	return nil
}

func isErrAssign(line string) bool {
	fields := strings.Fields(line)
	for i, f := range fields {
		if f == ":=" {
			if i > 0 {
				prev := fields[i-1]
				if prev == "err" || strings.HasSuffix(prev, ",err") {
					return true
				}
			}
		} else if strings.HasSuffix(f, ":=") {
			base := strings.TrimSuffix(f, ":=")
			if base == "err" || strings.HasSuffix(base, ",err") {
				return true
			}
		} else if idx := strings.Index(f, ":="); idx != -1 {
			base := f[:idx]
			if base == "err" || strings.HasSuffix(base, ",err") {
				return true
			}
		}
	}
	return false
}

// isIfErrCheck reports whether the line contains an `if err != nil {` check.
// It matches tokens produced by strings.Fields (e.g. ["if", "err", "!=", "nil", "{"]).
// Note: Unspaced forms like "if err!=nil{" are intentionally not matched because Go diffs
// are formatted with gofmt, where tokens are always space-separated.
func isIfErrCheck(line string) bool {
	fields := strings.Fields(line)
	for i := 0; i < len(fields); i++ {
		if fields[i] == "if" {
			if i+3 < len(fields) && fields[i+1] == "err" && fields[i+2] == "!=" {
				if fields[i+3] == "nil{" {
					return true
				}
				if fields[i+3] == "nil" && i+4 < len(fields) && (fields[i+4] == "{" || strings.HasPrefix(fields[i+4], "{")) {
					return true
				}
			}
		}
	}
	return false
}

func checkErrorDoubleSpacingAt(lines []string, i int, filename string) error {
	if i+2 >= len(lines) {
		return nil
	}
	l1 := lines[i]
	l2 := lines[i+1]
	l3 := lines[i+2]

	if strings.HasPrefix(l1, "+") && strings.HasPrefix(l2, "+") && strings.HasPrefix(l3, "+") {
		if !strings.HasPrefix(l1, "+++") && isErrAssign(l1[1:]) &&
			strings.TrimSpace(l2[1:]) == "" &&
			isIfErrCheck(l3[1:]) {
			return fmt.Errorf("detected double-spaced code in %s: blank line between error assignment and if err != nil check", filename)
		}
	}
	return nil
}
