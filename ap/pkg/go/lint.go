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
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/codestyle/pkg/walker"
	"k8s.io/klog/v2"
)

// Lint runs go vet and govulncheck in discovered modules.
func Lint(ctx context.Context, root string) error {
	cfg, err := config.Load(root)
	if err != nil {
		return err
	}

	// Find all go.mod files
	ignoreList := walker.NewIgnoreList([]string{".git", "vendor", "node_modules"})
	goMods, err := walker.Walk(root, ignoreList, func(_ string, info os.FileInfo) bool {
		return info.Name() == "go.mod"
	})
	if err != nil {
		return err
	}

	for _, goMod := range goMods {
		dir := filepath.Dir(goMod)

		if cfg.IsGovetEnabled() {
			klog.Infof("Running go vet in %s", dir)
			vetCmd := exec.CommandContext(ctx, "go", "vet", "./...")
			vetCmd.Dir = dir
			vetCmd.Stdout = os.Stdout
			vetCmd.Stderr = os.Stderr
			if err := vetCmd.Run(); err != nil {
				return fmt.Errorf("go vet failed in %s: %w", dir, err)
			}
		}

		if cfg.IsGovulncheckEnabled() {
			klog.Infof("Running govulncheck in %s", dir)
			vulnCmd := exec.CommandContext(ctx, "go", "run", "golang.org/x/vuln/cmd/govulncheck@latest", "./...")
			vulnCmd.Dir = dir
			vulnCmd.Stdout = os.Stdout
			vulnCmd.Stderr = os.Stderr
			if err := vulnCmd.Run(); err != nil {
				return fmt.Errorf("govulncheck failed in %s: %w", dir, err)
			}
		}

		if cfg.IsUnusedEnabled() {
			klog.Infof("Running unused check in %s", dir)
			apPath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("could not find ap executable: %w", err)
			}
			unusedCmd := exec.CommandContext(ctx, apPath, "unused", "./...")
			unusedCmd.Dir = dir
			unusedCmd.Stdout = os.Stdout
			unusedCmd.Stderr = os.Stderr
			if err := unusedCmd.Run(); err != nil {
				return fmt.Errorf("unused check failed in %s: %w", dir, err)
			}
		}
	}
	return nil
}
