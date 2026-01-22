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

	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

func Run(ctx context.Context, root string) error {
	// 1. Run legacy scripts
	if err := runLegacyScripts(ctx, root); err != nil {
		return err
	}

	// 2. Run built-in generators
	if err := runGenerateVerifierGenerator(ctx, root); err != nil {
		return err
	}

	if err := runApTestGenerator(ctx, root); err != nil {
		return err
	}

	if err := runApE2eGenerator(ctx, root); err != nil {
		return err
	}

	if err := runGithubActionsGenerator(ctx, root); err != nil {
		return err
	}

	return nil
}

func runLegacyScripts(ctx context.Context, root string) error {
	tasksDir := filepath.Join(root, "dev", "tasks")
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
			cmd.Dir = root
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("failed to run %s: %w", name, err)
			}
		}
	}
	return nil
}

func runGenerateVerifierGenerator(ctx context.Context, root string) error {
	presubmitsDir := filepath.Join(root, "dev", "ci", "presubmits")

	// Check if dev/ci/presubmits exists
	if _, err := os.Stat(presubmitsDir); os.IsNotExist(err) {
		return nil
	}

	targetFile := filepath.Join(presubmitsDir, "ap-verify-generate")
	klog.Infof("Generating %s", targetFile)

	apCmd, err := getApCommand(root)
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
	if err := os.WriteFile(targetFile, []byte(content), 0755); err != nil {
		return fmt.Errorf("failed to write %s: %w", targetFile, err)
	}

	return nil
}

func runApTestGenerator(ctx context.Context, root string) error {
	presubmitsDir := filepath.Join(root, "dev", "ci", "presubmits")

	// Check if dev/ci/presubmits exists
	if _, err := os.Stat(presubmitsDir); os.IsNotExist(err) {
		return nil
	}

	targetFile := filepath.Join(presubmitsDir, "ap-test")
	klog.Infof("Generating %s", targetFile)

	apCmd, err := getApCommand(root)
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
	if err := os.WriteFile(targetFile, []byte(content), 0755); err != nil {
		return fmt.Errorf("failed to write %s: %w", targetFile, err)
	}

	return nil
}

func runApE2eGenerator(ctx context.Context, root string) error {
	// Check if we have any e2e tasks
	e2eTasks, err := tasks.FindTaskScripts(root, tasks.WithPrefix("test-e2e"))
	if err != nil {
		return fmt.Errorf("failed to discover e2e tasks: %w", err)
	}

	presubmitsDir := filepath.Join(root, "dev", "ci", "presubmits")
	targetFile := filepath.Join(presubmitsDir, "ap-e2e")

	// If no e2e tasks, we should remove the file if it exists
	if len(e2eTasks) == 0 {
		if _, err := os.Stat(targetFile); err == nil {
			klog.Infof("Removing %s as no e2e tasks found", targetFile)
			if err := os.Remove(targetFile); err != nil {
				return fmt.Errorf("failed to remove %s: %w", targetFile, err)
			}
		}
		return nil
	}

	// Check if dev/ci/presubmits exists
	if _, err := os.Stat(presubmitsDir); os.IsNotExist(err) {
		return nil
	}

	klog.Infof("Generating %s", targetFile)

	apCmd, err := getApCommand(root)
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
	if err := os.WriteFile(targetFile, []byte(content), 0755); err != nil {
		return fmt.Errorf("failed to write %s: %w", targetFile, err)
	}

	return nil
}

func runGithubActionsGenerator(ctx context.Context, root string) error {
	presubmitsDir := filepath.Join(root, "dev", "ci", "presubmits")
	workflowsDir := filepath.Join(root, ".github", "workflows")
	outputFile := filepath.Join(workflowsDir, "ci-presubmits.yaml")

	klog.Infof("Generating %s", outputFile)

	entries, err := os.ReadDir(presubmitsDir)
	if os.IsNotExist(err) {
		// If no presubmits, maybe we shouldn't generate the workflow?
		// Or generate an empty one? The original script would iterate over nothing.
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to read presubmits dir: %w", err)
	}

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

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		scriptName := entry.Name()
		// Basic validation or filtering if needed

		sb.WriteString(fmt.Sprintf(`  %s:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout code
        uses: actions/checkout@v4

      - name: Setup Go
        uses: actions/setup-go@v5
        with:
          go-version-file: 'go.mod'

      - name: Run %s
        run: ./dev/ci/presubmits/%s

`, scriptName, scriptName, scriptName))
	}

	if err := os.MkdirAll(workflowsDir, 0755); err != nil {
		return fmt.Errorf("failed to create workflows dir: %w", err)
	}

	if err := os.WriteFile(outputFile, []byte(sb.String()), 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", outputFile, err)
	}

	return nil
}

func getApCommand(root string) (string, error) {
	configPath := filepath.Join(root, ".ap", "ap.yaml")
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

	return defaultCmd, nil
}
