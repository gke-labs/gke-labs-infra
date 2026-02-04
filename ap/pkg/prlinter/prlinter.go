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
	"regexp"
	"strings"

	"k8s.io/klog/v2"
)

// Lint runs PR-specific linting checks.
func Lint(ctx context.Context, repoRoot string) error {
	baseBranch, err := detectBaseBranch(ctx, repoRoot)
	if err != nil {
		klog.V(2).Infof("Could not detect base branch: %v", err)
		return nil
	}

	if baseBranch == "" {
		klog.V(2).Info("No base branch detected, skipping PR lint")
		return nil
	}

	klog.Infof("Comparing against base branch %q", baseBranch)

	diff, err := getDiff(ctx, repoRoot, baseBranch)
	if err != nil {
		return fmt.Errorf("error getting diff: %w", err)
	}

	if err := checkDoubleSpacing(diff); err != nil {
		return err
	}

	return nil
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

var errAssignRegex = regexp.MustCompile(`\berr\s*:=\s*`)
var ifErrCheckRegex = regexp.MustCompile(`if\s+err\s*!=\s*nil\s*\{`)

func checkErrorDoubleSpacingAt(lines []string, i int, filename string) error {
	if i+2 >= len(lines) {
		return nil
	}
	l1 := lines[i]
	l2 := lines[i+1]
	l3 := lines[i+2]

	if strings.HasPrefix(l1, "+") && strings.HasPrefix(l2, "+") && strings.HasPrefix(l3, "+") {
		if !strings.HasPrefix(l1, "+++") && errAssignRegex.MatchString(l1[1:]) &&
			strings.TrimSpace(l2[1:]) == "" &&
			ifErrCheckRegex.MatchString(l3[1:]) {
			return fmt.Errorf("detected double-spaced code in %s: blank line between error assignment and if err != nil check", filename)
		}
	}
	return nil
}
