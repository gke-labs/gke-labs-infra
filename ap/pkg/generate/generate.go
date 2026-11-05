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
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/fileheaders"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	golang "github.com/gke-labs/gke-labs-infra/ap/pkg/go"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/images"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"k8s.io/klog/v2"
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
	actionUploadArtifact = "actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # ratchet:actions/upload-artifact@v7.0.2"
)

// LegacyScriptTask represents a task to run a legacy generate script.
type LegacyScriptTask struct {
	Name string
	Path string
	Dir  string
}

func (t *LegacyScriptTask) Run(ctx context.Context, scope *tasks.TaskContext) error {
	klog.Infof("Running legacy generate script: %s", t.Name)
	cmd := exec.CommandContext(ctx, t.Path)
	cmd.Dir = t.Dir
	env, err := scope.ScriptEnv()
	if err != nil {
		return fmt.Errorf("failed to prepare script environment for %s: %w", t.Name, err)
	}
	cmd.Env = env
	cmd.Stdout = scope.Stdout()
	cmd.Stderr = scope.Stderr()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run %s: %w", t.Name, err)
	}
	if err := scope.SourceTree().InvalidateAll(); err != nil {
		return fmt.Errorf("failed to invalidate repo tree after %s: %w", t.Name, err)
	}
	return nil
}

func (t *LegacyScriptTask) GetName() string {
	return fmt.Sprintf("legacy-generate-%s", t.Name)
}

func (t *LegacyScriptTask) GetChildren() []tasks.Task {
	return nil
}

// GoGenerateTask represents a task to run `go generate ./...` in a module directory.
type GoGenerateTask struct {
	Dir string
}

func (t *GoGenerateTask) Run(ctx context.Context, scope *tasks.TaskContext) error {
	klog.Infof("Running go generate in %s", t.Dir)
	cmd := exec.CommandContext(ctx, "go", "generate", "./...")
	cmd.Dir = t.Dir
	cmd.Stdout = scope.Stdout()
	cmd.Stderr = scope.Stderr()
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go generate failed in %s: %w", t.Dir, err)
	}
	if err := scope.SourceTree().InvalidateAll(); err != nil {
		return fmt.Errorf("failed to invalidate repo tree after go generate in %s: %w", t.Dir, err)
	}
	return nil
}

func (t *GoGenerateTask) GetName() string {
	return fmt.Sprintf("go-generate-%s", filepath.Base(t.Dir))
}

func (t *GoGenerateTask) GetChildren() []tasks.Task {
	return nil
}

// BuiltinGeneratorTask represents a task to run a built-in generator.
type BuiltinGeneratorTask struct {
	Name    string
	RunFunc func(ctx context.Context, r *sourcetree.SourceTree, repoRoot string) error
}

func (t *BuiltinGeneratorTask) Run(ctx context.Context, scope *tasks.TaskContext) error {
	klog.Infof("Running built-in generator: %s", t.Name)
	return t.RunFunc(ctx, scope.SourceTree(), scope.RepoRoot())
}

func (t *BuiltinGeneratorTask) GetName() string {
	return fmt.Sprintf("builtin-generator-%s", t.Name)
}

func (t *BuiltinGeneratorTask) GetChildren() []tasks.Task {
	return nil
}

func hasGoGenerateInModule(r *sourcetree.SourceTree, mod string) bool {
	if r == nil {
		return false
	}
	for _, f := range r.Files() {
		if f.Module() == mod && f.HasGoGenerate() {
			return true
		}
	}
	return false
}

// GenerateTasks returns a task group for all generation tasks.
func GenerateTasks(repoRoot string, scopes []*tasks.TaskContext) (tasks.Task, error) {
	var allTasks []tasks.Task

	for _, scope := range scopes {
		dir := scope.Dir()
		group := &tasks.Group{
			Name: fmt.Sprintf("generate-%s", filepath.Base(dir)),
		}

		r := scope.SourceTree()

		// 1. Run legacy scripts
		legacyScripts, err := tasks.FindTaskScripts(r, dir, tasks.WithPrefix("generate-"), tasks.WithExcludePrefix("generate-github-actions"))
		if err != nil {
			return nil, err
		}
		for _, ts := range legacyScripts {
			script, ok := ts.(*tasks.TaskScript)
			if !ok {
				continue
			}
			group.Tasks = append(group.Tasks, &LegacyScriptTask{
				Name: script.Name,
				Path: script.Path,
				Dir:  script.Root,
			})
		}

		// 2. Run go generate in each discovered module that has //go:generate
		for _, mod := range r.Under(dir).Modules() {
			if hasGoGenerateInModule(r, mod) {
				group.Tasks = append(group.Tasks, &GoGenerateTask{
					Dir: mod,
				})
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
		Name:    "audit-generated",
		RunFunc: runAuditGeneratedGenerator,
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
		RunFunc: func(ctx context.Context, r *sourcetree.SourceTree, repoRoot string) error {
			return runApBuildGenerator(ctx, r, repoRoot, scopes)
		},
	})
	allTasks = append(allTasks, &BuiltinGeneratorTask{
		Name: "ap-e2e",
		RunFunc: func(ctx context.Context, r *sourcetree.SourceTree, repoRoot string) error {
			return runApE2eGenerator(ctx, r, repoRoot, scopes)
		},
	})
	allTasks = append(allTasks, &BuiltinGeneratorTask{
		Name: "github-actions",
		RunFunc: func(ctx context.Context, r *sourcetree.SourceTree, repoRoot string) error {
			return runGithubActionsGenerator(ctx, r, repoRoot, scopes)
		},
	})

	return &tasks.Group{
		Name:  "generate",
		Tasks: allTasks,
	}, nil
}

func Run(ctx context.Context, repoRoot string, scopes []*tasks.TaskContext) error {
	t, err := GenerateTasks(repoRoot, scopes)
	if err != nil {
		return err
	}
	var r *sourcetree.SourceTree
	if len(scopes) > 0 {
		r = scopes[0].SourceTree()
	}
	return t.Run(ctx, tasks.NewTaskContext(repoRoot, repoRoot, r))
}

func runGenerateVerifierGenerator(_ context.Context, r *sourcetree.SourceTree, repoRoot string) error {
	presubmitsDir := filepath.Join(repoRoot, "dev", "ci", "presubmits")

	targetFile := filepath.Join(presubmitsDir, "ap-verify-generate")
	klog.Infof("Generating %s", targetFile)

	if err := os.MkdirAll(presubmitsDir, 0755); err != nil {
		return fmt.Errorf("failed to create presubmits dir: %w", err)
	}

	apCmd, err := GetApCommand(repoRoot)
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

	var headerBlock string
	if headerContent != "" {
		headerBlock = headerContent + "\n"
	}

	content := fmt.Sprintf(`#!/bin/bash
# Code generated by ap generate. DO NOT EDIT.

%sset -o errexit
set -o nounset
set -o pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "${REPO_ROOT}"
%s
# Run generation in verify mode
%s generate --verify //...

# Check for changes
if [[ -n $(git status --porcelain) ]]; then
  echo "Changes detected after running 'ap generate'. Run '%s generate //...' and commit the result."
  echo "<hint_for_agent>Run '%s generate //...' to fix failures</hint_for_agent>"
  git status
  exit 1
fi
`, headerBlock, skipGuard, apCmd, apCmd, apCmd)
	if err := r.WriteFile(targetFile, []byte(content), 0755); err != nil {
		return fmt.Errorf("failed to write %s: %w", targetFile, err)
	}

	return nil
}

func runAuditGeneratedGenerator(_ context.Context, r *sourcetree.SourceTree, repoRoot string) error {
	rootCfg, err := config.LoadAPConfig(repoRoot)
	if err != nil {
		return err
	}
	presubmitsDir := filepath.Join(repoRoot, "dev", "ci", "presubmits")
	targetFile := filepath.Join(presubmitsDir, "ap-audit-generated")

	if rootCfg == nil || rootCfg.Generate == nil || !rootCfg.Generate.AuditGenerated {
		// If not opted in, remove stale ap-audit-generated if present
		if err := r.Remove(targetFile); err != nil {
			return fmt.Errorf("failed to remove %s: %w", targetFile, err)
		}
		return nil
	}

	klog.Infof("Generating %s", targetFile)

	if err := os.MkdirAll(presubmitsDir, 0755); err != nil {
		return fmt.Errorf("failed to create presubmits dir: %w", err)
	}

	apCmd, err := GetApCommand(repoRoot)
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

	var headerBlock string
	if headerContent != "" {
		headerBlock = headerContent + "\n"
	}

	content := fmt.Sprintf(`#!/bin/bash
# Code generated by ap generate. DO NOT EDIT.

%sset -o errexit
set -o nounset
set -o pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "${REPO_ROOT}"
%s
# Run audit mode (strict in CI)
%s generate --audit-generated --strict //...
`, headerBlock, skipGuard, apCmd)
	if err := r.WriteFile(targetFile, []byte(content), 0755); err != nil {
		return fmt.Errorf("failed to write %s: %w", targetFile, err)
	}

	return nil
}

func runApTestGenerator(_ context.Context, r *sourcetree.SourceTree, repoRoot string) error {
	presubmitsDir := filepath.Join(repoRoot, "dev", "ci", "presubmits")

	targetFile := filepath.Join(presubmitsDir, "ap-test")
	klog.Infof("Generating %s", targetFile)

	if err := os.MkdirAll(presubmitsDir, 0755); err != nil {
		return fmt.Errorf("failed to create presubmits dir: %w", err)
	}

	apCmd, err := GetApCommand(repoRoot)
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

	var headerBlock string
	if headerContent != "" {
		headerBlock = headerContent + "\n"
	}

	content := fmt.Sprintf(`#!/bin/bash
# Code generated by ap generate. DO NOT EDIT.

%sset -o errexit
set -o nounset
set -o pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "${REPO_ROOT}"
%s
# Run tests
%s test //...
`, headerBlock, skipGuard, apCmd)
	if err := r.WriteFile(targetFile, []byte(content), 0755); err != nil {
		return fmt.Errorf("failed to write %s: %w", targetFile, err)
	}

	return nil
}

func runApLintGenerator(_ context.Context, r *sourcetree.SourceTree, repoRoot string) error {
	presubmitsDir := filepath.Join(repoRoot, "dev", "ci", "presubmits")

	targetFile := filepath.Join(presubmitsDir, "ap-lint")
	klog.Infof("Generating %s", targetFile)

	if err := os.MkdirAll(presubmitsDir, 0755); err != nil {
		return fmt.Errorf("failed to create presubmits dir: %w", err)
	}

	apCmd, err := GetApCommand(repoRoot)
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

	var headerBlock string
	if headerContent != "" {
		headerBlock = headerContent + "\n"
	}

	content := fmt.Sprintf(`#!/bin/bash
# Code generated by ap generate. DO NOT EDIT.

%sset -o errexit
set -o nounset
set -o pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "${REPO_ROOT}"
%s
# Run linting
%s lint //...
`, headerBlock, skipGuard, apCmd)
	if err := r.WriteFile(targetFile, []byte(content), 0755); err != nil {
		return fmt.Errorf("failed to write %s: %w", targetFile, err)
	}

	return nil
}

func runApBuildGenerator(_ context.Context, r *sourcetree.SourceTree, repoRoot string, scopes []*tasks.TaskContext) error {
	// Check if any apRoot has any images to build OR any build-* scripts
	hasBuild := false
	for _, scope := range scopes {
		apRoot := scope.Dir()
		scopeTree := scope.SourceTree()
		ok, err := images.HasImages(scopeTree, apRoot)
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
		exists, err := fileutils.FileExists(targetFile)
		if err != nil {
			return err
		}
		if exists {
			klog.Infof("Removing %s as no build tasks found", targetFile)
			if err := r.Remove(targetFile); err != nil {
				return fmt.Errorf("failed to remove %s: %w", targetFile, err)
			}
		}
		return nil
	}

	klog.Infof("Generating %s", targetFile)

	if err := os.MkdirAll(presubmitsDir, 0755); err != nil {
		return fmt.Errorf("failed to create presubmits dir: %w", err)
	}

	apCmd, err := GetApCommand(repoRoot)
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

	var headerBlock string
	if headerContent != "" {
		headerBlock = headerContent + "\n"
	}

	content := fmt.Sprintf(`#!/bin/bash
# Code generated by ap generate. DO NOT EDIT.

%sset -o errexit
set -o nounset
set -o pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "${REPO_ROOT}"
%s
# Run build
%s build //...
`, headerBlock, skipGuard, apCmd)
	if err := r.WriteFile(targetFile, []byte(content), 0755); err != nil {
		return fmt.Errorf("failed to write %s: %w", targetFile, err)
	}

	return nil
}

// isGeneratedByApGenerate reports whether the file at path carries the
// "Code generated by ap generate. DO NOT EDIT." (or legacy "Generated by ap generate. DO NOT EDIT.") header.
func isGeneratedByApGenerate(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	checkLen := len(data)
	if checkLen > 4096 {
		checkLen = 4096
	}

	lines := strings.Split(string(data[:checkLen]), "\n")
	if len(lines) > 30 {
		lines = lines[:30]
	}

	for _, line := range lines {
		if isGen, header := sourcetree.IsGeneratedLine(line); isGen {
			if strings.Contains(header, "by ap generate") || strings.Contains(header, "by ap-generate") {
				return true, nil
			}
		}
	}
	return false, nil
}

func runApE2eGenerator(_ context.Context, r *sourcetree.SourceTree, repoRoot string, scopes []*tasks.TaskContext) error {
	presubmitsDir := filepath.Join(repoRoot, "dev", "ci", "presubmits")

	type e2eTarget struct {
		target string
		suffix string
	}

	targets := make(map[string]e2eTarget)

	for _, scope := range scopes {
		for _, t := range scope.E2ETasks {
			if ts, ok := t.(*tasks.TaskScript); ok {
				rel, err := filepath.Rel(repoRoot, ts.Root)
				if err != nil {
					continue
				}
				if rel == "." {
					targets["."] = e2eTarget{target: ".", suffix: ""}
				} else {
					suffix := "-" + strings.ReplaceAll(filepath.ToSlash(rel), "/", "-")
					targets[rel] = e2eTarget{target: rel, suffix: suffix}
				}
			} else if gt, ok := t.(*golang.GoE2eTask); ok {
				rel, err := filepath.Rel(repoRoot, gt.Dir)
				if err != nil {
					continue
				}
				if rel == "." {
					targets["."] = e2eTarget{target: ".", suffix: ""}
				} else {
					suffix := "-" + strings.ReplaceAll(filepath.ToSlash(rel), "/", "-")
					targets[rel] = e2eTarget{target: rel, suffix: suffix}
				}
			}
		}
	}

	desiredFiles := make(map[string]bool)
	for _, tgt := range targets {
		targetFile := filepath.Join(presubmitsDir, "ap-e2e"+tgt.suffix)
		desiredFiles[targetFile] = true
	}

	// Clean up any ap-e2e* presubmits that are not in desiredFiles
	entries, err := os.ReadDir(presubmitsDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read presubmits dir: %w", err)
	}
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if strings.HasPrefix(e.Name(), "ap-e2e") {
				p := filepath.Join(presubmitsDir, e.Name())
				if !desiredFiles[p] {
					isGen, err := isGeneratedByApGenerate(p)
					if err != nil {
						return fmt.Errorf("failed to check if %s is generated: %w", p, err)
					}
					if !isGen {
						klog.Infof("Keeping %s as it was not generated by ap generate", p)
						continue
					}
					klog.Infof("Removing %s as no matching e2e tasks found", p)
					if err := r.Remove(p); err != nil {
						return fmt.Errorf("failed to remove %s: %w", p, err)
					}
				}
			}
		}
	}

	if len(targets) == 0 {
		return nil
	}

	if err := os.MkdirAll(presubmitsDir, 0755); err != nil {
		return fmt.Errorf("failed to create presubmits dir: %w", err)
	}

	apCmd, err := GetApCommand(repoRoot)
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

	var headerBlock string
	if headerContent != "" {
		headerBlock = headerContent + "\n"
	}

	var keys []string
	for k := range targets {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		tgt := targets[k]
		targetFile := filepath.Join(presubmitsDir, "ap-e2e"+tgt.suffix)
		klog.Infof("Generating %s", targetFile)

		content := fmt.Sprintf(`#!/bin/bash
# Code generated by ap generate. DO NOT EDIT.

%sset -o errexit
set -o nounset
set -o pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "${REPO_ROOT}"
%s
# Run e2e tests
%s e2e %s
`, headerBlock, skipGuard, apCmd, tgt.target)
		if err := r.WriteFile(targetFile, []byte(content), 0755); err != nil {
			return fmt.Errorf("failed to write %s: %w", targetFile, err)
		}
	}

	return nil
}

func runGithubActionsGenerator(_ context.Context, r *sourcetree.SourceTree, repoRoot string, scopes []*tasks.TaskContext) error {
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
	sb.WriteString("# Code generated by ap generate. DO NOT EDIT.\n\n")
	sb.WriteString(headerContent)
	sb.WriteString(`name: CI Presubmits

on:
  push:
    branches:
      - main
  pull_request:
  merge_group:

concurrency:
  group: ${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}
  cancel-in-progress: ${{ github.event_name == 'pull_request' }}

# Presubmits only read the repo; an explicit least-privilege grant
# also keeps zizmor's excessive-permissions audit clean.
permissions:
  contents: read

jobs:
`)

	presubmitsDir := filepath.Join(repoRoot, "dev", "ci", "presubmits")
	entries, err := os.ReadDir(presubmitsDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read presubmits dir %s: %w", presubmitsDir, err)
	}

	goModExists := r.FileExists(filepath.Join(repoRoot, "go.mod"))
	relPresubmitsDir, err := filepath.Rel(repoRoot, presubmitsDir)
	if err != nil {
		return err
	}

	var scriptNames []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		scriptNames = append(scriptNames, entry.Name())
	}
	sort.Strings(scriptNames)

	for _, scriptName := range scriptNames {
		jobName := scriptName

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
			sb.WriteString(fmt.Sprintf(`
      - name: Setup Go
        uses: %s
        with:
          go-version-file: 'go.mod'
`, actionSetupGo))
		}

		if scriptName == "ap-build" {
			cleanupTaskPath := filepath.Join(repoRoot, "dev", "tasks", "free-disk-space-on-github-actions-runner")
			hasCleanup, err := fileutils.FileExists(cleanupTaskPath)
			if err != nil {
				return err
			}
			if hasCleanup {
				relCleanupTask, err := filepath.Rel(repoRoot, cleanupTaskPath)
				if err != nil {
					return fmt.Errorf("failed to get relative path for cleanup task: %w", err)
				}
				sb.WriteString(fmt.Sprintf(`
      - name: Free disk space
        run: ./%s
`, relCleanupTask))
			}
		}

		sb.WriteString(fmt.Sprintf(`
      - name: Run %s
        run: ./%s/%s

      - name: Upload artifacts
        if: always()
        uses: %s
        with:
          name: artifacts-%s
          path: /tmp/artifacts
          if-no-files-found: ignore
`, jobName, relPresubmitsDir, scriptName, actionUploadArtifact, jobName))
	}

	if err := os.MkdirAll(workflowsDir, 0755); err != nil {
		return fmt.Errorf("failed to create workflows dir: %w", err)
	}

	if err := r.WriteFile(outputFile, []byte(sb.String()), 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", outputFile, err)
	}

	return nil
}

func GetApCommand(repoRoot string) (string, error) {
	defaultCmd := "go run github.com/gke-labs/gke-labs-infra/ap@latest"

	cfg, err := config.LoadAPConfig(repoRoot)
	if err != nil {
		return "", err
	}
	if cfg == nil {
		return defaultCmd, nil
	}

	if cfg.Version == "!self" {
		return "go run ./ap", nil
	}

	// A concrete version (e.g. "v0.12.3", or a pseudo-version) pins
	// the generated wrappers and workflow to that ap release, so
	// upstream ap releases can't invalidate the consumer repo's
	// ap-verify-generate check. "latest" (and empty) keeps the
	// floating behavior.
	if cfg.Version != "" && cfg.Version != "latest" {
		return fmt.Sprintf("go run github.com/gke-labs/gke-labs-infra/ap@%s", cfg.Version), nil
	}

	return defaultCmd, nil
}

// loadPresubmitSkipPatterns returns presubmits.skipIfOnlyChanged from
// .ap/ap.yaml, or nil when the file or the setting is absent.
func loadPresubmitSkipPatterns(repoRoot string) ([]string, error) {
	cfg, err := config.LoadAPConfig(repoRoot)
	if err != nil {
		return nil, err
	}
	if cfg == nil || cfg.Presubmits == nil {
		return nil, nil
	}

	patterns := cfg.Presubmits.SkipIfOnlyChanged
	for _, p := range patterns {
		if !presubmitSkipPatternOK(p) {
			return nil, fmt.Errorf("%s: presubmits.skipIfOnlyChanged pattern %q: only letters, digits, and / . _ - * ? ! [ ] are allowed", filepath.Join(repoRoot, ".ap", "ap.yaml"), p)
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
