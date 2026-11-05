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

package gostyle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/cache"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/walker"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"k8s.io/klog/v2"
)

var tidyRunner = func(ctx context.Context, r *sourcetree.SourceTree, dir string) error {
	cmd := exec.CommandContext(ctx, "go", "mod", "tidy")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go mod tidy failed in %s: %w\n%s", dir, err, strings.TrimSpace(string(out)))
	}
	if r != nil {
		r.Track(filepath.Join(dir, "go.mod"), filepath.Join(dir, "go.sum"))
	}
	return nil
}

// Run runs gostyle formatting and mod tidy, directing command output to the provided writers.
func Run(ctx context.Context, r *sourcetree.SourceTree, repoRoot string, files []string, stdout, stderr io.Writer) error {
	log := klog.FromContext(ctx)

	// Initialize cache
	cm, err := cache.NewManager()
	if err != nil {
		log.V(2).Info("Failed to initialize cache", "error", err)
	} else {
		defer func() {
			if err := cm.Save(); err != nil {
				log.Error(err, "Failed to save cache")
			}
		}()
	}

	cfg, err := config.Effective(repoRoot, repoRoot)
	if err != nil {
		return err
	}

	if err := runGofmt(ctx, r, repoRoot, files, cfg.Skip, cm, stdout, stderr); err != nil {
		return err
	}

	if err := runGoModTidy(ctx, r, repoRoot, files, cfg.Skip, cm); err != nil {
		return err
	}

	return nil
}

func runGofmt(ctx context.Context, r *sourcetree.SourceTree, repoRoot string, files []string, skip []string, cm *cache.Manager, stdout, stderr io.Writer) error {
	log := klog.FromContext(ctx)

	ignoreList := walker.NewIgnoreList(append([]string{"vendor", ".git"}, skip...))
	expandedFiles, err := walker.ExpandPaths(r, repoRoot, files, ignoreList)
	if err != nil {
		return err
	}

	moduleConfigs := make(map[string]*config.Config)
	for _, modDir := range r.Modules() {
		modCfg, err := config.Effective(repoRoot, modDir)
		if err != nil {
			return err
		}
		moduleConfigs[modDir] = modCfg
	}
	if _, ok := moduleConfigs[repoRoot]; !ok {
		rootCfg, err := config.Effective(repoRoot, repoRoot)
		if err != nil {
			return err
		}
		moduleConfigs[repoRoot] = rootCfg
	}

	var filesToFormat []string
	for _, f := range expandedFiles {
		if strings.HasSuffix(f, ".go") {
			file := r.File(f)
			modDir := repoRoot
			if file != nil && file.Module() != "" {
				modDir = file.Module()
			}
			if modCfg := moduleConfigs[modDir]; modCfg != nil && !modCfg.IsGofmtEnabled() {
				continue
			}
			filesToFormat = append(filesToFormat, f)
		}
	}

	// Filter files using cache
	var dirtyFiles []string
	if cm != nil {
		for _, f := range filesToFormat {
			file := r.File(f)
			if file == nil {
				dirtyFiles = append(dirtyFiles, f)
				continue
			}
			hash, err := file.Hash()
			if err != nil {
				dirtyFiles = append(dirtyFiles, f)
				continue
			}
			if !cm.IsDone(cache.StepGofmt, hash) {
				dirtyFiles = append(dirtyFiles, f)
			}
		}
	} else {
		dirtyFiles = filesToFormat
	}

	if len(dirtyFiles) == 0 {
		return nil
	}

	log.Info("Running gofmt", "files", len(dirtyFiles))

	// Chunk files to avoid argument length limits
	chunkSize := 100
	for i := 0; i < len(dirtyFiles); i += chunkSize {
		end := i + chunkSize
		if end > len(dirtyFiles) {
			end = len(dirtyFiles)
		}
		chunk := dirtyFiles[i:end]

		args := append([]string{"-w"}, chunk...)
		cmd := exec.CommandContext(ctx, "gofmt", args...)
		cmd.Stdout = stdout
		cmd.Stderr = stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("gofmt failed: %w", err)
		}
	}

	// Update cache for processed files
	if r != nil {
		r.Track(dirtyFiles...)
	}
	for _, f := range dirtyFiles {
		file := r.File(f)
		if file != nil {
			if cm != nil {
				hash, err := file.Hash()
				if err != nil {
					continue
				}
				cm.MarkDone(cache.StepGofmt, hash)
			}
		}
	}

	return nil
}

func runGoModTidy(ctx context.Context, r *sourcetree.SourceTree, repoRoot string, files []string, skip []string, cm *cache.Manager) error {
	log := klog.FromContext(ctx)

	ignoreList := walker.NewIgnoreList(append([]string{"vendor", ".git"}, skip...))
	expandedFiles, err := walker.ExpandPaths(r, repoRoot, files, ignoreList)
	if err != nil {
		return err
	}

	inScopeModules := make(map[string]bool)
	for _, f := range expandedFiles {
		if mod := r.ModuleFor(f); mod != "" {
			inScopeModules[mod] = true
		}
	}

	for _, modDir := range r.Modules() {
		if !inScopeModules[modDir] {
			continue
		}

		modCfg, err := config.Effective(repoRoot, modDir)
		if err != nil {
			return err
		}
		if !modCfg.IsTidyEnabled() {
			continue
		}

		relMod, err := filepath.Rel(repoRoot, modDir)
		if err != nil {
			continue
		}

		goFiles := r.Under(modDir).Files(func(f *sourcetree.File) bool {
			if !strings.HasSuffix(f.Name, ".go") || f.Module() != modDir {
				return false
			}
			rel, err := filepath.Rel(repoRoot, f.Path)
			if err != nil {
				return false
			}
			return !ignoreList.ShouldIgnore(filepath.ToSlash(rel), false)
		})

		key, err := computeModuleKey(modDir, r, goFiles)
		if err != nil {
			return err
		}

		if cm != nil && cm.IsDone(cache.StepTidy, key) {
			log.V(2).Info("Module is tidy (cached)", "module", relMod)
			continue
		}

		log.Info("Running go mod tidy", "module", relMod)

		// In multi-module repositories or repositories with a go.work file, running
		// `go mod tidy` in workspace mode can cause dependencies to be resolved
		// across workspace siblings or lead to discrepancies between local workspace
		// builds and external/isolated builds. Disabling workspace mode with GOWORK=off
		// ensures that each module is tidied strictly on its own terms as an independent module.
		if err := tidyRunner(ctx, r, modDir); err != nil {
			return err
		}

		if cm != nil {
			postKey, err := computeModuleKey(modDir, r, goFiles)
			if err != nil {
				return err
			}
			cm.MarkDone(cache.StepTidy, postKey)
		}
	}

	return nil
}

func computeModuleKey(modDir string, r *sourcetree.SourceTree, goFiles []*sourcetree.File) (string, error) {
	h := sha256.New()
	if f := r.File(filepath.Join(modDir, "go.mod")); f != nil {
		hash, err := f.Hash()
		if err != nil {
			return "", err
		}
		h.Write([]byte("go.mod:" + hash + "\n"))
	}
	if f := r.File(filepath.Join(modDir, "go.sum")); f != nil {
		hash, err := f.Hash()
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err == nil {
			h.Write([]byte("go.sum:" + hash + "\n"))
		}
	}
	for _, f := range goFiles {
		hash, err := f.Hash()
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(modDir, f.Path)
		if err != nil {
			return "", err
		}
		h.Write([]byte(filepath.ToSlash(rel) + ":" + hash + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
