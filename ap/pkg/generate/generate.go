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

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/images"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

// LegacyScriptTask represents a task to run a legacy generate script.
type LegacyScriptTask struct {
	Name string
	Path string
	Dir  string
}

func (t *LegacyScriptTask) Run(ctx context.Context, _ string) error {
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
	Name string
	RunFunc func(ctx context.Context, repoRoot string) error
}

func (t *BuiltinGeneratorTask) Run(ctx context.Context, repoRoot string) error {
	klog.Infof("Running built-in generator: %s", t.Name)
	return t.RunFunc(ctx, repoRoot)
}

func (t *BuiltinGeneratorTask) GetName() string {
	return fmt.Sprintf("builtin-generator-%s", t.Name)
}

func (t *BuiltinGeneratorTask) GetChildren() []tasks.Task {
	return nil
}

// GenerateTasks returns a task group for all generation tasks.
func GenerateTasks(repoRoot string) (tasks.Task, error) {
	apRoots, err := config.FindAllAPRoots(repoRoot)
	if err != nil {
		return nil, err
	}

	var allTasks []tasks.Task

	for _, apRoot := range apRoots {
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
		Name: "verify-generate",
		RunFunc: runGenerateVerifierGenerator,
	})
	allTasks = append(allTasks, &BuiltinGeneratorTask{
		Name: "ap-test",
		RunFunc: runApTestGenerator,
	})
	allTasks = append(allTasks, &BuiltinGeneratorTask{
		Name: "ap-lint",
		RunFunc: runApLintGenerator,
	})
	allTasks = append(allTasks, &BuiltinGeneratorTask{
		Name: "ap-build",
		RunFunc: func(ctx context.Context, repoRoot string) error {
			return runApBuildGenerator(ctx, repoRoot, apRoots)
		},
	})
	allTasks = append(allTasks, &BuiltinGeneratorTask{
		Name: "ap-e2e",
		RunFunc: func(ctx context.Context, repoRoot string) error {
			return runApE2eGenerator(ctx, repoRoot, apRoots)
		},
	})
	allTasks = append(allTasks, &BuiltinGeneratorTask{
		Name: "github-actions",
		RunFunc: func(ctx context.Context, repoRoot string) error {
			return runGithubActionsGenerator(ctx, repoRoot, apRoots)
		},
	})

	return &tasks.Group{
		Name:  "generate",
		Tasks: allTasks,
	}, nil
}

func Run(ctx context.Context, repoRoot string) error {
	t, err := GenerateTasks(repoRoot)
	if err != nil {
		return err
	}
	return t.Run(ctx, repoRoot)
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

	content := fmt.Sprintf(`#!/bin/bash

# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit
set -o nounset
set -o pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "${REPO_ROOT}"

# Run generation
%s generate

# Check for changes
if [[ -n $(git status --porcelain) ]]; then
  echo "Changes detected after running 'ap generate'. Please commit these changes."
  echo "<hint_for_agent>Run '%s generate' to fix failures</hint_for_agent>"
  git status
  exit 1
fi
`, apCmd, apCmd)
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

	content := fmt.Sprintf(`#!/bin/bash

# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit
set -o nounset
set -o pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "${REPO_ROOT}"

# Run tests
%s test
`, apCmd)
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

	content := fmt.Sprintf(`#!/bin/bash

# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit
set -o nounset
set -o pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "${REPO_ROOT}"

# Run linting
%s lint
`, apCmd)
	if err := writeFileIfChanged(targetFile, []byte(content), 0755); err != nil {
		return fmt.Errorf("failed to write %s: %w", targetFile, err)
	}

	return nil
}

func runApBuildGenerator(_ context.Context, repoRoot string, apRoots []string) error {
	// Check if any apRoot has any images to build OR any build-* scripts
	hasBuild := false
	for _, apRoot := range apRoots {
		ok, err := images.HasImages(apRoot)
		if err == nil && ok {
			hasBuild = true
			break
		}

		buildTasks, err := tasks.FindTaskScripts(apRoot, tasks.WithPrefix("build-"))
		if err == nil && len(buildTasks) > 0 {
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

	content := fmt.Sprintf(`#!/bin/bash

# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit
set -o nounset
set -o pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "${REPO_ROOT}"

# Run build
%s build
`, apCmd)
	if err := writeFileIfChanged(targetFile, []byte(content), 0755); err != nil {
		return fmt.Errorf("failed to write %s: %w", targetFile, err)
	}

	return nil
}

func runApE2eGenerator(_ context.Context, repoRoot string, apRoots []string) error {
	// Check if any apRoot has any e2e tasks
	hasE2e := false
	for _, apRoot := range apRoots {
		e2eTasks, err := tasks.FindTaskScripts(apRoot, tasks.WithPrefix("test-e2e"))
		if err != nil {
			return fmt.Errorf("failed to discover e2e tasks in %s: %w", apRoot, err)
		}
		if len(e2eTasks) > 0 {
			hasE2e = true
			break
		}
	}

	presubmitsDir := filepath.Join(repoRoot, "dev", "ci", "presubmits")
	targetFile := filepath.Join(presubmitsDir, "ap-e2e")

	// If no e2e tasks, we should remove the file if it exists
	if !hasE2e {
		if _, err := os.Stat(targetFile); err == nil {
			klog.Infof("Removing %s as no e2e tasks found", targetFile)
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

	content := fmt.Sprintf(`#!/bin/bash

# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit
set -o nounset
set -o pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "${REPO_ROOT}"

# Run e2e tests
%s e2e
`, apCmd)
	if err := writeFileIfChanged(targetFile, []byte(content), 0755); err != nil {
		return fmt.Errorf("failed to write %s: %w", targetFile, err)
	}

	return nil
}

func runGithubActionsGenerator(_ context.Context, repoRoot string, apRoots []string) error {
	workflowsDir := filepath.Join(repoRoot, ".github", "workflows")
	outputFile := filepath.Join(workflowsDir, "ci-presubmits.yaml")

	klog.Infof("Generating %s", outputFile)

	var sb strings.Builder
	sb.WriteString(`# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

name: CI Presubmits

on:
  push:
    branches:
      - main
  pull_request:
  merge_group:

jobs:
`)

	for _, apRoot := range apRoots {
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
    steps:
      - name: Checkout code
        uses: actions/checkout@v4
`, jobName))

			if goModExists {
				relGoMod, _ := filepath.Rel(repoRoot, filepath.Join(apRoot, "go.mod"))
				sb.WriteString(fmt.Sprintf(`
      - name: Setup Go
        uses: actions/setup-go@v5
        with:
          go-version-file: '%s'
`, relGoMod))
			}

			sb.WriteString(fmt.Sprintf(`
      - name: Run %s
        run: ./%s/%s

`, jobName, relPresubmitsDir, scriptName))
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
		rel, err := filepath.Rel(apRoot, repoRoot)
		if err != nil {
			return "go run ./ap", nil
		}
		apDir := filepath.Join(rel, "ap")
		if !strings.HasPrefix(apDir, ".") && !filepath.IsAbs(apDir) {
			apDir = "./" + apDir
		}
		return fmt.Sprintf("go run %s", apDir), nil
	}

	return defaultCmd, nil
}

func writeFileIfChanged(path string, content []byte, perm os.FileMode) error {
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, content) {
		return nil
	}
	return os.WriteFile(path, content, perm)
}
