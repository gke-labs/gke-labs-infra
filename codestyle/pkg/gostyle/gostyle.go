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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/codestyle/pkg/cache"
	"github.com/gke-labs/gke-labs-infra/codestyle/pkg/walker"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

type GovetConfig struct {
	Enabled bool `json:"enabled"`
}

type Config struct {
	Gofmt bool         `json:"gofmt"`
	Govet *GovetConfig `json:"govet"`
	Skip  []string     `json:"skip"`
}

func Run(ctx context.Context, repoRoot string, files []string) error {
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

	configFile := filepath.Join(repoRoot, ".codestyle/go.yaml")

	// Check if config exists
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		log.V(2).Info("No .codestyle/go.yaml found, skipping go formatting")
		return nil
	}

	data, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("error reading %s: %w", configFile, err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("error parsing %s: %w", configFile, err)
	}

	if config.Gofmt {
		if err := runGofmt(ctx, repoRoot, files, config.Skip, cm); err != nil {
			return err
		}
	}

	if config.Govet != nil && config.Govet.Enabled {
		if err := runGoVet(ctx, repoRoot, config.Skip); err != nil {
			return err
		}
	}

	return nil
}

func runGofmt(ctx context.Context, repoRoot string, files []string, skip []string, cm *cache.Manager) error {
	log := klog.FromContext(ctx)
	var filesToFormat []string
	if len(files) > 0 {
		for _, f := range files {
			if strings.HasSuffix(f, ".go") {
				absPath := f
				if !filepath.IsAbs(f) {
					absPath = filepath.Join(repoRoot, f)
				}
				filesToFormat = append(filesToFormat, absPath)
			}
		}
	} else {
		fv := walker.NewFileView(repoRoot, append([]string{"vendor", ".git"}, skip...))
		err := fv.Walk(func(f walker.File) error {
			if strings.HasSuffix(f.Path, ".go") {
				filesToFormat = append(filesToFormat, f.Path)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("error walking for go files: %w", err)
		}
	}

	// Filter files using cache
	var dirtyFiles []string
	if cm != nil {
		for _, f := range filesToFormat {
			meta, err := cm.GetOrUpdateMetadata(f)
			if err != nil {
				dirtyFiles = append(dirtyFiles, f)
				continue
			}
			if !cm.IsGofmtDone(meta.Hash) {
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
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("gofmt failed: %w", err)
		}
	}

	// Update cache for processed files
	if cm != nil {
		for _, f := range dirtyFiles {
			// Re-check metadata. gofmt might have changed it.
			meta, err := cm.GetOrUpdateMetadata(f)
			if err != nil {
				continue
			}
			cm.MarkGofmtDone(meta.Hash)
		}
	}

	return nil
}

func runGoVet(ctx context.Context, repoRoot string, skip []string) error {
	log := klog.FromContext(ctx)
	log.Info("Running go vet")

	fv := walker.NewFileView(repoRoot, append([]string{"vendor", ".git"}, skip...))
	var goModFiles []string
	err := fv.Walk(func(f walker.File) error {
		if f.Info.Name() == "go.mod" {
			goModFiles = append(goModFiles, f.Path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("error walking for go.mod files: %w", err)
	}

	var goModDirs []string
	for _, f := range goModFiles {
		goModDirs = append(goModDirs, filepath.Dir(f))
	}

	for _, dir := range goModDirs {
		log.Info("Running go vet", "dir", dir)
		cmd := exec.CommandContext(ctx, "go", "vet", "./...")
		cmd.Dir = dir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("go vet failed in %s: %w", dir, err)
		}
	}
	return nil
}
