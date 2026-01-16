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

package format

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/codestyle/pkg/fileheaders"
	"github.com/gke-labs/gke-labs-infra/codestyle/pkg/gostyle"
	"k8s.io/klog/v2"
)

func Run(ctx context.Context, root string) error {
	// 1. Run codestyle (headers, gofmt, etc)
	if err := runCodestyle(ctx, root); err != nil {
		return err
	}

	// 2. Run legacy format scripts
	if err := runLegacyScripts(ctx, root); err != nil {
		return err
	}

	return nil
}

func runCodestyle(ctx context.Context, root string) error {
	codestyleDir := filepath.Join(root, ".codestyle")

	// Check if .codestyle exists
	if _, err := os.Stat(codestyleDir); os.IsNotExist(err) {
		return nil
	}

	klog.Info("Running codestyle...")
	if err := fileheaders.Run(ctx, root, nil); err != nil {
		return fmt.Errorf("fileheaders failed: %w", err)
	}
	if err := gostyle.Run(ctx, root, nil); err != nil {
		return fmt.Errorf("gostyle failed: %w", err)
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
		if strings.HasPrefix(name, "format-") && !entry.IsDir() {
			path := filepath.Join(tasksDir, name)
			klog.Infof("Running legacy format script: %s", name)
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
