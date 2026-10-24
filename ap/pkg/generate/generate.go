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
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/fileheaders"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/images"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/repo"
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

func (t *LegacyScriptTask) Run(ctx context.Context, scope *tasks.APScope) error {
	klog.Infof("Running legacy generate script: %s", t.Name)
	cmd := exec.CommandContext(ctx, t.Path)
	cmd.Dir = t.Dir
	env, err := tasks.ScriptEnv()
	if err != nil {
		return fmt.Errorf("failed to prepare script environment for %s: %w", t.Name, err)
	}
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run %s: %w", t.Name, err)
	}
	if scope != nil {
		if r, err := scope.GetRepo(); err == nil && r != nil {
			if err := r.InvalidateAll(); err != nil {
				return fmt.Errorf("failed to invalidate repo tree after %s: %w", t.Name, err)
			}
		}
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

func (t *GoGenerateTask) Run(ctx context.Context, scope *tasks.APScope) error {
	klog.Infof("Running go generate in %s", t.Dir)
	cmd := exec.CommandContext(ctx, "go", "generate", "./...")
	cmd.Dir = t.Dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go generate failed in %s: %w", t.Dir, err)
	}
	if scope != nil {
		if r, err := scope.GetRepo(); err == nil && r != nil {
			if err := r.InvalidateAll(); err != nil {
				return fmt.Errorf("failed to invalidate repo tree after go generate in %s: %w", t.Dir, err)
			}
		}
	}
	return nil
}

func (t *GoGenerateTask) GetName() string {
	return fmt.Sprintf("go-generate-%s", filepath.Base(t.Dir))
}

func (t *GoGenerateTask) GetChildren() []tasks.Task {
	return nil
}

func hasGoGenerate(r *repo.Repo, apRoot string) bool {
	if r == nil {
		return false
	}
	for _, f := range r.Files() {
		if f.APRoot() == apRoot || f.Module() == apRoot {
			if f.HasGoGenerate() {
				return true
			}
		}
	}
	return false
}

// BuiltinGeneratorTask represents a task to run a built-in generator.
type BuiltinGeneratorTask struct {
	Name    string
	RunFunc func(ctx context.Context, r *repo.Repo, repoRoot string) error
}

func (t *BuiltinGeneratorTask) Run(ctx context.Context, scope *tasks.APScope) error {
	klog.Infof("Running built-in generator: %s", t.Name)
	var r *repo.Repo
	if scope != nil {
		var err error
		r, err = scope.GetRepo()
		if err != nil {
			return err
		}
	}
	return t.RunFunc(ctx, r, scope.RepoRoot)
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

		r, err := scope.GetRepo()
		if err != nil {
			return nil, err
		}

		// 1. Run legacy scripts
		tasksDir := filepath.Join(apRoot, "dev", "tasks")
		for _, f := range r.FilesInDir(tasksDir) {
			name := f.Name
			if strings.HasPrefix(name, "generate-") {
				// Skip generate-github-actions as we are replacing it
				if name == "generate-github-actions" {
					continue
				}

				group.Tasks = append(group.Tasks, &LegacyScriptTask{
					Name: name,
					Path: f.Path,
					Dir:  apRoot,
				})
			}
		}

		// 2. Run go generate if any Go file in the scope has //go:generate
		if hasGoGenerate(r, apRoot) {
			group.Tasks = append(group.Tasks, &GoGenerateTask{
				Dir: apRoot,
			})
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
		RunFunc: func(ctx context.Context, r *repo.Repo, repoRoot string) error {
			return runApBuildGenerator(ctx, r, repoRoot, scopes)
		},
	})
	allTasks = append(allTasks, &BuiltinGeneratorTask{
		Name: "ap-e2e",
		RunFunc: func(ctx context.Context, r *repo.Repo, repoRoot string) error {
			return runApE2eGenerator(ctx, r, repoRoot, scopes)
		},
	})
	allTasks = append(allTasks, &BuiltinGeneratorTask{
		Name: "github-actions",
		RunFunc: func(ctx context.Context, r *repo.Repo, repoRoot string) error {
			return runGithubActionsGenerator(ctx, r, repoRoot, scopes)
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
	var r *repo.Repo
	if len(scopes) > 0 {
		var scopeErr error
		r, scopeErr = scopes[0].GetRepo()
		if scopeErr != nil {
			return scopeErr
		}
	}
	if r == nil {
		var openErr error
		r, openErr = repo.Open(repoRoot)
		if openErr != nil {
			return openErr
		}
	}
	return t.Run(ctx, &tasks.APScope{RepoRoot: repoRoot, Dir: repoRoot, Repo: r})
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

func runGenerateVerifierGenerator(_ context.Context, r *repo.Repo, repoRoot string) error {
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

func runAuditGeneratedGenerator(_ context.Context, r *repo.Repo, repoRoot string) error {
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

func runApTestGenerator(_ context.Context, r *repo.Repo, repoRoot string) error {
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

func runApLintGenerator(_ context.Context, r *repo.Repo, repoRoot string) error {
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

func runApBuildGenerator(_ context.Context, r *repo.Repo, repoRoot string, scopes []*tasks.APScope) error {
	// Check if any apRoot has any images to build OR any build-* scripts
	hasBuild := false
	for _, scope := range scopes {
		apRoot := scope.Dir
		scopeRepo, err := scope.GetRepo()
		if err != nil {
			return err
		}
		ok, err := images.HasImages(scopeRepo, apRoot)
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

func runApE2eGenerator(_ context.Context, r *repo.Repo, repoRoot string, scopes []*tasks.APScope) error {
	presubmitsDir := filepath.Join(repoRoot, "dev", "ci", "presubmits")

	// Remove the global ap-e2e script if it exists
	globalTargetFile := filepath.Join(presubmitsDir, "ap-e2e")
	exists, err := fileutils.FileExists(globalTargetFile)
	if err != nil {
		return err
	}
	if exists {
		klog.Infof("Removing global %s", globalTargetFile)
		if err := r.Remove(globalTargetFile); err != nil {
			return fmt.Errorf("failed to remove %s: %w", globalTargetFile, err)
		}
	}

	for _, scope := range scopes {
		apRoot := scope.Dir
		e2eTasks := scope.E2ETasks

		suffix := getSuffix(repoRoot, apRoot)
		targetFile := filepath.Join(presubmitsDir, "ap-e2e"+suffix)

		if len(e2eTasks) == 0 {
			exists, err := fileutils.FileExists(targetFile)
			if err != nil {
				return err
			}
			if exists {
				klog.Infof("Removing %s as no e2e tasks found", targetFile)
				if err := r.Remove(targetFile); err != nil {
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
# Run e2e tests
%s e2e %s
`, headerBlock, skipGuard, apCmd, relApRoot)
		if err := r.WriteFile(targetFile, []byte(content), 0755); err != nil {
			return fmt.Errorf("failed to write %s: %w", targetFile, err)
		}
	}

	return nil
}

func runGithubActionsGenerator(_ context.Context, r *repo.Repo, repoRoot string, scopes []*tasks.APScope) error {
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

# Presubmits only read the repo; an explicit least-privilege grant
# also keeps zizmor's excessive-permissions audit clean.
permissions:
  contents: read

jobs:
`)

	for _, scope := range scopes {
		apRoot := scope.Dir
		scopeRepo, err := scope.GetRepo()
		if err != nil {
			return err
		}

		suffix := getSuffix(repoRoot, apRoot)
		presubmitsDir := filepath.Join(apRoot, "dev", "ci", "presubmits")
		// Read directly from the live filesystem here: earlier generators
		// in this same run (verify-generate, ap-test, ap-lint, ap-build, ap-e2e,
		// and any legacy generate-* scripts) write presubmit scripts to
		// dev/ci/presubmits, so the pre-run in-memory repo tree snapshot does
		// not contain them on a fresh run.
		entries, err := os.ReadDir(presubmitsDir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("failed to read presubmits dir %s: %w", presubmitsDir, err)
		}

		goModExists := scopeRepo.FileExists(filepath.Join(apRoot, "go.mod"))

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
				relGoMod, err := filepath.Rel(repoRoot, filepath.Join(apRoot, "go.mod"))
				if err != nil {
					return fmt.Errorf("failed to get relative path for go.mod: %w", err)
				}
				sb.WriteString(fmt.Sprintf(`
      - name: Setup Go
        uses: %s
        with:
          go-version-file: '%s'
`, actionSetupGo, relGoMod))
			}

			if scriptName == "ap-build" {
				cleanupTaskPath := filepath.Join(apRoot, "dev", "tasks", "free-disk-space-on-github-actions-runner")
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
	}

	if err := os.MkdirAll(workflowsDir, 0755); err != nil {
		return fmt.Errorf("failed to create workflows dir: %w", err)
	}

	if err := r.WriteFile(outputFile, []byte(sb.String()), 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", outputFile, err)
	}

	return nil
}

func GetApCommand(repoRoot, apRoot string) (string, error) {
	defaultCmd := "go run github.com/gke-labs/gke-labs-infra/ap@latest"

	cfg, err := config.LoadAPConfig(apRoot)
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
