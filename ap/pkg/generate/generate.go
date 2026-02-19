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

func Run(ctx context.Context, repoRoot string) error {
	apRoots, err := config.FindAllAPRoots(repoRoot)
	if err != nil {
		return err
	}

	for _, apRoot := range apRoots {
		klog.Infof("Generating for AP root: %s", apRoot)

		// 1. Run legacy scripts
		if err := runLegacyScripts(ctx, apRoot); err != nil {
			return err
		}
	}

	// 2. Run built-in generators (only in repoRoot)
	if err := runGenerateVerifierGenerator(ctx, repoRoot); err != nil {
		return err
	}

	if err := runApTestGenerator(ctx, repoRoot); err != nil {
		return err
	}

	if err := runApLintGenerator(ctx, repoRoot); err != nil {
		return err
	}

	if err := runApBuildGenerator(ctx, repoRoot, apRoots); err != nil {
		return err
	}

	if err := runApE2eGenerator(ctx, repoRoot, apRoots); err != nil {
		return err
	}

	if err := runGithubActionsGenerator(ctx, repoRoot, apRoots); err != nil {
		return err
	}

	return nil
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

func runLegacyScripts(ctx context.Context, apRoot string) error {
	tasksDir := filepath.Join(apRoot, "dev", "tasks")
	entries, err := os.ReadDir(tasksDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to read tasks dir: %w", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "generate-") && !entry.IsDir() {
			// Skip generate-github-actions as we are replacing it
			if name == "generate-github-actions" {
				continue
			}

			path := filepath.Join(tasksDir, name)
			klog.Infof("Running legacy generate script: %s", name)
			cmd := exec.CommandContext(ctx, path)
			cmd.Dir = apRoot
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("failed to run %s: %w", name, err)
			}
		}
	}
	return nil
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
