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

package generate

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/fileheaders"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/images"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

// GitHub Actions used in generated workflows, pinned to full commit
// SHAs. Org security scanning (zizmor, blanket hash-pin policy)
// rejects mutable tag refs in workflows, and consumers can't pin by
// hand because ap-verify-generate requires byte-equality with fresh
// generator output — so the pins must live here. The trailing
// comment is ratchet syntax (github.com/sethvargo/ratchet), the same
// convention Google's scan workflows use; bump by resolving the new
// tag's commit SHA and updating both the SHA and the comment.
const (
	actionCheckout       = "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # ratchet:actions/checkout@v7.0.1"
	actionSetupGo        = "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # ratchet:actions/setup-go@v7.0.0"
	actionUploadArtifact = "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # ratchet:actions/upload-artifact@v7.0.1"
)

// LegacyScriptTask represents a task to run a legacy generate script.
type LegacyScriptTask struct {
	Name string
	Path string
	Dir  string
}

func (t *LegacyScriptTask) Run(ctx context.Context, scope *tasks.APScope) error {
	klog.Infof("Running legacy generate script: %s", t.Name)
	cmd := exec.CommandContext(ctx, t.Path)
	cmd.Dir = t.Dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run %s: %w", t.Name, err)
	}
	return nil
}

func (t *LegacyScriptTask) GetName() string {
	return fmt.Sprintf("legacy-generate-%s", t.Name)
}

func (t *LegacyScriptTask) GetChildren() []tasks.Task {
	return nil
}

// BuiltinGeneratorTask represents a task to run a built-in generator.
type BuiltinGeneratorTask struct {
	Name    string
	RunFunc func(ctx context.Context, repoRoot string) error
}

func (t *BuiltinGeneratorTask) Run(ctx context.Context, scope *tasks.APScope) error {
	klog.Infof("Running built-in generator: %s", t.Name)
	return t.RunFunc(ctx, scope.RepoRoot)
}

func (t *BuiltinGeneratorTask) GetName() string {
	return fmt.Sprintf("builtin-generator-%s", t.Name)
}

func (t *BuiltinGeneratorTask) GetChildren() []tasks.Task {
	return nil
}

// GenerateTasks returns a task group for all generation tasks.
func GenerateTasks(repoRoot string, scopes []*tasks.APScope) (tasks.Task, error) {
	var allTasks []tasks.Task

	for _, scope := range scopes {
		apRoot := scope.Dir
		group := &tasks.Group{
			Name: fmt.Sprintf("generate-%s", filepath.Base(apRoot)),
		}

		// 1. Run legacy scripts
		tasksDir := filepath.Join(apRoot, "dev", "tasks")
		entries, err := os.ReadDir(tasksDir)
		if err == nil {
			for _, entry := range entries {
				name := entry.Name()
				if strings.HasPrefix(name, "generate-") && !entry.IsDir() {
					// Skip generate-github-actions as we are replacing it
					if name == "generate-github-actions" {
						continue
					}

					group.Tasks = append(group.Tasks, &LegacyScriptTask{
						Name: name,
						Path: filepath.Join(tasksDir, name),
						Dir:  apRoot,
					})
				}
			}
		}

		if len(group.Tasks) > 0 {
			allTasks = append(allTasks, group)
		}
	}

	// 2. Run built-in generators
	allTasks = append(allTasks, &BuiltinGeneratorTask{
		Name:    "verify-generate",
		RunFunc: runGenerateVerifierGenerator,
	})
	allTasks = append(allTasks, &BuiltinGeneratorTask{
		Name:    "ap-test",
		RunFunc: runApTestGenerator,
	})
	allTasks = append(allTasks, &BuiltinGeneratorTask{
		Name:    "ap-lint",
		RunFunc: runApLintGenerator,
	})
	allTasks = append(allTasks, &BuiltinGeneratorTask{
		Name: "ap-build",
		RunFunc: func(ctx context.Context, repoRoot string) error {
			return runApBuildGenerator(ctx, repoRoot, scopes)
		},
	})
	allTasks = append(allTasks, &BuiltinGeneratorTask{
		Name: "ap-e2e",
		RunFunc: func(ctx context.Context, repoRoot string) error {
			return runApE2eGenerator(ctx, repoRoot, scopes)
		},
	})
	allTasks = append(allTasks, &BuiltinGeneratorTask{
		Name: "github-actions",
		RunFunc: func(ctx context.Context, repoRoot string) error {
			return runGithubActionsGenerator(ctx, repoRoot, scopes)
		},
	})

	return &tasks.Group{
		Name:  "generate",
		Tasks: allTasks,
	}, nil
}

func Run(ctx context.Context, repoRoot string, scopes []*tasks.APScope) error {
	t, err := GenerateTasks(repoRoot, scopes)
	if err != nil {
		return err
	}
	return t.Run(ctx, &tasks.APScope{RepoRoot: repoRoot, Dir: repoRoot})
}

func getSuffix(repoRoot, apRoot string) string {
	if repoRoot == apRoot {
		return ""
	}
	rel, err := filepath.Rel(repoRoot, apRoot)
	if err != nil {
		return ""
	}
	return "-" + strings.ReplaceAll(rel, string(filepath.Separator), "-")
}

func runGenerateVerifierGenerator(_ context.Context, repoRoot string) error {
	presubmitsDir := filepath.Join(repoRoot, "dev", "ci", "presubmits")

	targetFile := filepath.Join(presubmitsDir, "ap-verify-generate")
	klog.Infof("Generating %s", targetFile)

	if err := os.MkdirAll(presubmitsDir, 0755); err != nil {
		return fmt.Errorf("failed to create presubmits dir: %w", err)
	}

	apCmd, err := GetApCommand(repoRoot, repoRoot)
	if err != nil {
		return err
	}

	headers, err := config.LoadHeaders(repoRoot)
	if err != nil {
		return fmt.Errorf("failed to load headers: %w", err)
	}
	headerContent, err := fileheaders.GenerateHeader(headers, "#")
	if err != nil {
		return fmt.Errorf("failed to generate header: %w", err)
	}

	skipGuard, err := presubmitSkipGuardFromConfig(repoRoot)
	if err != nil {
		return err
	}

	content := fmt.Sprintf(`#!/bin/bash

%s
set -o errexit
set -o nounset
set -o pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "${REPO_ROOT}"
%s
# Run generation
%s generate //...

# Check for changes
if [[ -n $(git status --porcelain) ]]; then
  echo "Changes detected after running 'ap generate'. Please commit these changes."
  echo "<hint_for_agent>Run '%s generate //...' to fix failures</hint_for_agent>"
  git status
  exit 1
fi
`, headerContent, skipGuard, apCmd, apCmd)
	if err := writeFileIfChanged(targetFile, []byte(content), 0755); err != nil {
		return fmt.Errorf("failed to write %s: %w", targetFile, err)
	}

	return nil
}

func runApTestGenerator(_ context.Context, repoRoot string) error {
	presubmitsDir := filepath.Join(repoRoot, "dev", "ci", "presubmits")

	targetFile := filepath.Join(presubmitsDir, "ap-test")
	klog.Infof("Generating %s", targetFile)

	if err := os.MkdirAll(presubmitsDir, 0755); err != nil {
		return fmt.Errorf("failed to create presubmits dir: %w", err)
	}

	apCmd, err := GetApCommand(repoRoot, repoRoot)
	if err != nil {
		return err
	}

	headers, err := config.LoadHeaders(repoRoot)
	if err != nil {
		return fmt.Errorf("failed to load headers: %w", err)
	}
	headerContent, err := fileheaders.GenerateHeader(headers, "#")
	if err != nil {
		return fmt.Errorf("failed to generate header: %w", err)
	}

	skipGuard, err := presubmitSkipGuardFromConfig(repoRoot)
	if err != nil {
		return err
	}

	content := fmt.Sprintf(`#!/bin/bash

%s
set -o errexit
set -o nounset
set -o pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "${REPO_ROOT}"
%s
# Run tests
%s test //...
`, headerContent, skipGuard, apCmd)
	if err := writeFileIfChanged(targetFile, []byte(content), 0755); err != nil {
		return fmt.Errorf("failed to write %s: %w", targetFile, err)
	}

	return nil
}

func runApLintGenerator(_ context.Context, repoRoot string) error {
	presubmitsDir := filepath.Join(repoRoot, "dev", "ci", "presubmits")

	targetFile := filepath.Join(presubmitsDir, "ap-lint")
	klog.Infof("Generating %s", targetFile)

	if err := os.MkdirAll(presubmitsDir, 0755); err != nil {
		return fmt.Errorf("failed to create presubmits dir: %w", err)
	}

	apCmd, err := GetApCommand(repoRoot, repoRoot)
	if err != nil {
		return err
	}

	headers, err := config.LoadHeaders(repoRoot)
	if err != nil {
		return fmt.Errorf("failed to load headers: %w", err)
	}
	headerContent, err := fileheaders.GenerateHeader(headers, "#")
	if err != nil {
		return fmt.Errorf("failed to generate header: %w", err)
	}

	skipGuard, err := presubmitSkipGuardFromConfig(repoRoot)
	if err != nil {
		return err
	}

	content := fmt.Sprintf(`#!/bin/bash

%s
set -o errexit
set -o nounset
set -o pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "${REPO_ROOT}"
%s
# Run linting
%s lint //...
`, headerContent, skipGuard, apCmd)
	if err := writeFileIfChanged(targetFile, []byte(content), 0755); err != nil {
		return fmt.Errorf("failed to write %s: %w", targetFile, err)
	}

	return nil
}

func runApBuildGenerator(_ context.Context, repoRoot string, scopes []*tasks.APScope) error {
	// Check if any apRoot has any images to build OR any build-* scripts
	hasBuild := false
	for _, scope := range scopes {
		apRoot := scope.Dir
		ok, err := images.HasImages(apRoot)
		if err == nil && ok {
			hasBuild = true
			break
		}

		buildTasks := scope.BuildTasks
		if len(buildTasks) > 0 {
			hasBuild = true
			break
		}
	}

	presubmitsDir := filepath.Join(repoRoot, "dev", "ci", "presubmits")
	targetFile := filepath.Join(presubmitsDir, "ap-build")

	// If no images or build scripts, we should remove the file if it exists
	if !hasBuild {
		if _, err := os.Stat(targetFile); err == nil {
			klog.Infof("Removing %s as no build tasks found", targetFile)
			if err := os.Remove(targetFile); err != nil {
				return fmt.Errorf("failed to remove %s: %w", targetFile, err)
			}
		}
		return nil
	}

	klog.Infof("Generating %s", targetFile)

	if err := os.MkdirAll(presubmitsDir, 0755); err != nil {
		return fmt.Errorf("failed to create presubmits dir: %w", err)
	}

	apCmd, err := GetApCommand(repoRoot, repoRoot)
	if err != nil {
		return err
	}

	headers, err := config.LoadHeaders(repoRoot)
	if err != nil {
		return fmt.Errorf("failed to load headers: %w", err)
	}
	headerContent, err := fileheaders.GenerateHeader(headers, "#")
	if err != nil {
		return fmt.Errorf("failed to generate header: %w", err)
	}

	skipGuard, err := presubmitSkipGuardFromConfig(repoRoot)
	if err != nil {
		return err
	}

	content := fmt.Sprintf(`#!/bin/bash

%s
set -o errexit
set -o nounset
set -o pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "${REPO_ROOT}"
%s
# Run build
%s build //...
`, headerContent, skipGuard, apCmd)
	if err := writeFileIfChanged(targetFile, []byte(content), 0755); err != nil {
		return fmt.Errorf("failed to write %s: %w", targetFile, err)
	}

	return nil
}

func runApE2eGenerator(_ context.Context, repoRoot string, scopes []*tasks.APScope) error {
	presubmitsDir := filepath.Join(repoRoot, "dev", "ci", "presubmits")

	// Remove the global ap-e2e script if it exists
	globalTargetFile := filepath.Join(presubmitsDir, "ap-e2e")
	if _, err := os.Stat(globalTargetFile); err == nil {
		klog.Infof("Removing global %s", globalTargetFile)
		if err := os.Remove(globalTargetFile); err != nil {
			return fmt.Errorf("failed to remove %s: %w", globalTargetFile, err)
		}
	}

	for _, scope := range scopes {
		apRoot := scope.Dir
		e2eTasks := scope.E2ETasks

		suffix := getSuffix(repoRoot, apRoot)
		targetFile := filepath.Join(presubmitsDir, "ap-e2e"+suffix)

		if len(e2eTasks) == 0 {
			if _, err := os.Stat(targetFile); err == nil {
				klog.Infof("Removing %s as no e2e tasks found", targetFile)
				if err := os.Remove(targetFile); err != nil {
					return fmt.Errorf("failed to remove %s: %w", targetFile, err)
				}
			}
			continue
		}

		klog.Infof("Generating %s", targetFile)

		if err := os.MkdirAll(presubmitsDir, 0755); err != nil {
			return fmt.Errorf("failed to create presubmits dir: %w", err)
		}

		apCmd, err := GetApCommand(repoRoot, apRoot)
		if err != nil {
			return err
		}

		relApRoot, err := filepath.Rel(repoRoot, apRoot)
		if err != nil {
			return fmt.Errorf("failed to get relative path: %w", err)
		}
		headers, err := config.LoadHeaders(repoRoot)
		if err != nil {
			return fmt.Errorf("failed to load headers: %w", err)
		}
		headerContent, err := fileheaders.GenerateHeader(headers, "#")
		if err != nil {
			return fmt.Errorf("failed to generate header: %w", err)
		}

		skipGuard, err := presubmitSkipGuardFromConfig(repoRoot)
		if err != nil {
			return err
		}

		content := fmt.Sprintf(`#!/bin/bash

%s
set -o errexit
set -o nounset
set -o pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "${REPO_ROOT}"
%s
# Run e2e tests
%s e2e %s
`, headerContent, skipGuard, apCmd, relApRoot)
		if err := writeFileIfChanged(targetFile, []byte(content), 0755); err != nil {
			return fmt.Errorf("failed to write %s: %w", targetFile, err)
		}
	}

	return nil
}

func runGithubActionsGenerator(_ context.Context, repoRoot string, scopes []*tasks.APScope) error {
	workflowsDir := filepath.Join(repoRoot, ".github", "workflows")
	outputFile := filepath.Join(workflowsDir, "ci-presubmits.yaml")

	klog.Infof("Generating %s", outputFile)

	var sb strings.Builder
	headers, err := config.LoadHeaders(repoRoot)
	if err != nil {
		return fmt.Errorf("failed to load headers: %w", err)
	}
	headerContent, err := fileheaders.GenerateHeader(headers, "#")
	if err != nil {
		return fmt.Errorf("failed to generate header: %w", err)
	}
	sb.WriteString(headerContent)
	sb.WriteString(`# Generated by ap generate. DO NOT EDIT.

name: CI Presubmits

on:
  push:
    branches:
      - main
  pull_request:
  merge_group:

# Presubmits only read the repo; an explicit least-privilege grant
# also keeps zizmor's excessive-permissions audit clean.
permissions:
  contents: read

jobs:
`)

	for _, scope := range scopes {
		apRoot := scope.Dir
		suffix := getSuffix(repoRoot, apRoot)
		presubmitsDir := filepath.Join(apRoot, "dev", "ci", "presubmits")
		entries, err := os.ReadDir(presubmitsDir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("failed to read presubmits dir %s: %w", presubmitsDir, err)
		}

		goModExists := false
		if _, err := os.Stat(filepath.Join(apRoot, "go.mod")); err == nil {
			goModExists = true
		}

		relPresubmitsDir, err := filepath.Rel(repoRoot, presubmitsDir)
		if err != nil {
			return err
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			scriptName := entry.Name()

			jobName := scriptName
			if suffix != "" && !strings.HasSuffix(jobName, suffix) {
				jobName = jobName + suffix
			}

			sb.WriteString(fmt.Sprintf(`  %s:
    runs-on: ubuntu-latest
    env:
      ARTIFACTS: /tmp/artifacts
    steps:
      - name: Checkout code
        uses: %s
        with:
          # Presubmit scripts never push; don't leave the token in
          # .git/config (zizmor: artipacked).
          persist-credentials: false
`, jobName, actionCheckout))

			if goModExists {
				relGoMod, _ := filepath.Rel(repoRoot, filepath.Join(apRoot, "go.mod"))
				sb.WriteString(fmt.Sprintf(`
      - name: Setup Go
        uses: %s
        with:
          go-version-file: '%s'
`, actionSetupGo, relGoMod))
			}

			if scriptName == "ap-build" {
				cleanupTaskPath := filepath.Join(apRoot, "dev", "tasks", "free-disk-space-on-github-actions-runner")
				if _, err := os.Stat(cleanupTaskPath); err == nil {
					relCleanupTask, _ := filepath.Rel(repoRoot, cleanupTaskPath)
					sb.WriteString(fmt.Sprintf(`
      - name: Free disk space
        run: ./%s
`, relCleanupTask))
				}
			}

			sb.WriteString(fmt.Sprintf(`
      - name: Run %s
        run: ./%s/%s
`, jobName, relPresubmitsDir, scriptName))

			if strings.Contains(scriptName, "test") || strings.Contains(scriptName, "e2e") {
				sb.WriteString(fmt.Sprintf(`
      - name: Upload artifacts
        if: always()
        uses: %s
        with:
          name: artifacts-%s
          path: /tmp/artifacts
`, actionUploadArtifact, jobName))
			}
		}
	}

	if err := os.MkdirAll(workflowsDir, 0755); err != nil {
		return fmt.Errorf("failed to create workflows dir: %w", err)
	}

	if err := writeFileIfChanged(outputFile, []byte(sb.String()), 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", outputFile, err)
	}

	return nil
}

func GetApCommand(repoRoot, apRoot string) (string, error) {
	configPath := filepath.Join(apRoot, ".ap", "ap.yaml")
	defaultCmd := "go run github.com/gke-labs/gke-labs-infra/ap@latest"

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return defaultCmd, nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", configPath, err)
	}

	var config struct {
		Version string `json:"version"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("failed to parse %s: %w", configPath, err)
	}

	if config.Version == "!self" {
		return "go run ./ap", nil
	}

	// A concrete version (e.g. "v0.12.3", or a pseudo-version) pins
	// the generated wrappers and workflow to that ap release, so
	// upstream ap releases can't invalidate the consumer repo's
	// ap-verify-generate check. "latest" (and empty) keeps the
	// floating behavior.
	if config.Version != "" && config.Version != "latest" {
		return fmt.Sprintf("go run github.com/gke-labs/gke-labs-infra/ap@%s", config.Version), nil
	}

	return defaultCmd, nil
}

// loadPresubmitSkipPatterns returns presubmits.skipIfOnlyChanged from
// .ap/ap.yaml, or nil when the file or the setting is absent.
func loadPresubmitSkipPatterns(repoRoot string) ([]string, error) {
	configPath := filepath.Join(repoRoot, ".ap", "ap.yaml")
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", configPath, err)
	}

	var config struct {
		Presubmits struct {
			SkipIfOnlyChanged []string `json:"skipIfOnlyChanged"`
		} `json:"presubmits"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", configPath, err)
	}

	patterns := config.Presubmits.SkipIfOnlyChanged
	for _, p := range patterns {
		if !presubmitSkipPatternOK(p) {
			return nil, fmt.Errorf("%s: presubmits.skipIfOnlyChanged pattern %q: only letters, digits, and / . _ - * ? ! [ ] are allowed", configPath, p)
		}
	}
	return patterns, nil
}

// presubmitSkipPatternOK reports whether a skipIfOnlyChanged pattern
// is safe to splice into the generated bash `case` statement.
func presubmitSkipPatternOK(p string) bool {
	if p == "" {
		return false
	}
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("/._-*?![]", r):
		default:
			return false
		}
	}
	return true
}

// presubmitSkipGuardFromConfig renders the early-exit block spliced
// into every generated presubmit script, or "" when the repo doesn't
// configure presubmits.skipIfOnlyChanged (keeping generated output
// byte-identical for repos that don't opt in).
func presubmitSkipGuardFromConfig(repoRoot string) (string, error) {
	patterns, err := loadPresubmitSkipPatterns(repoRoot)
	if err != nil {
		return "", err
	}
	return presubmitSkipGuard(patterns), nil
}

// presubmitSkipGuard returns a bash fragment that exits the presubmit
// early (and successfully) when a pull request only changes files
// matching the given case globs. Exiting green — instead of a `paths:`
// filter on the generated workflow — keeps required status checks
// satisfied; a workflow skipped by `paths:` leaves them pending
// forever. The guard fails open: shallow history, a non-merge HEAD,
// or an empty diff all fall through to a full run, and push /
// merge_group events are never skipped, so the merge queue remains a
// complete gate.
func presubmitSkipGuard(patterns []string) string {
	if len(patterns) == 0 {
		return ""
	}
	return fmt.Sprintf(`
# Pull requests whose changed files all match the patterns in
# .ap/ap.yaml (presubmits.skipIfOnlyChanged) don't need this
# presubmit; exit early but successfully so required status checks
# stay green. The patterns are bash case globs matched against each
# repo-relative path (* also matches /). Anything uncertain — shallow
# history, a non-merge HEAD, an empty diff — falls through to a full
# run, and push / merge_group events always run everything.
if [[ "${GITHUB_EVENT_NAME:-}" == "pull_request" && -n "${GITHUB_SHA:-}" ]]; then
  if ! git rev-parse --verify --quiet "${GITHUB_SHA}^1" >/dev/null; then
    # actions/checkout does a depth-1 clone of the PR merge commit;
    # deepen it so the parents are available for diffing.
    git fetch --quiet --depth=2 origin "${GITHUB_SHA}" >/dev/null 2>&1 || true
  fi
  # Diffing the merge commit against its first parent yields exactly
  # the PR's changes; without a merge commit we can't tell, so run.
  if git rev-parse --verify --quiet "${GITHUB_SHA}^2" >/dev/null; then
    ap_changed="$(git diff --name-only "${GITHUB_SHA}^1" "${GITHUB_SHA}")" || ap_changed=""
    if [[ -n "${ap_changed}" ]]; then
      ap_skip="true"
      while IFS= read -r ap_file; do
        case "${ap_file}" in
        %s) ;;
        *)
          ap_skip="false"
          break
          ;;
        esac
      done <<<"${ap_changed}"
      if [[ "${ap_skip}" == "true" ]]; then
        echo "All changed files match presubmits.skipIfOnlyChanged; skipping $(basename "$0")."
        exit 0
      fi
    fi
  fi
fi
`, strings.Join(patterns, "|"))
}

func writeFileIfChanged(path string, content []byte, perm os.FileMode) error {
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, content) {
		return nil
	}
	return os.WriteFile(path, content, perm)
}
