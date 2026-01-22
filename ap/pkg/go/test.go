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

package golang

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/gke-labs/gke-labs-infra/codestyle/pkg/walker"
	"k8s.io/klog/v2"
)

// Test runs go tests in discovered modules.
func Test(ctx context.Context, root string) error {
	// Find all go.mod files
	ignoreList := walker.NewIgnoreList([]string{".git", "vendor", "node_modules"})
	goMods, err := walker.Walk(root, ignoreList, func(path string, info os.FileInfo) bool {
		return info.Name() == "go.mod"
	})
	if err != nil {
		return err
	}

	for _, goMod := range goMods {
		dir := filepath.Dir(goMod)

		// Check if there are any packages
		listCmd := exec.CommandContext(ctx, "go", "list", "./...")
		listCmd.Dir = dir
		output, err := listCmd.Output()
		if err != nil {
			return fmt.Errorf("go list failed in %s: %w", dir, err)
		}

		if len(bytes.TrimSpace(output)) == 0 {
			klog.Infof("No packages found in %s, skipping vet and test", dir)
			continue
		}

		klog.Infof("Running go vet in %s", dir)
		vetCmd := exec.CommandContext(ctx, "go", "vet", "./...")
		vetCmd.Dir = dir
		vetCmd.Stdout = os.Stdout
		vetCmd.Stderr = os.Stderr
		if err := vetCmd.Run(); err != nil {
			return fmt.Errorf("go vet failed in %s: %w", dir, err)
		}

		klog.Infof("Running go test in %s", dir)
		cmd := exec.CommandContext(ctx, "go", "test", "./...")
		cmd.Dir = dir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("go test failed in %s: %w", dir, err)
		}
	}
	return nil
}
