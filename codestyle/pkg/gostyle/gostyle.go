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

	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

type GovetConfig struct {
	Enabled bool `json:"enabled"`
}

type Config struct {
	Gofmt bool         `json:"gofmt"`
	Govet *GovetConfig `json:"govet"`
}

func Run(ctx context.Context, repoRoot string, files []string) error {
	log := klog.FromContext(ctx)

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
		if err := runGofmt(ctx, repoRoot, files); err != nil {
			return err
		}
	}

	if config.Govet != nil && config.Govet.Enabled {
		if err := runGoVet(ctx, repoRoot); err != nil {
			return err
		}
	}

	return nil
}

func runGofmt(ctx context.Context, repoRoot string, files []string) error {
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
		// Walk and find .go files
		err := filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if info.Name() == "vendor" || info.Name() == ".git" {
					return filepath.SkipDir
				}
			}
			if !info.IsDir() && strings.HasSuffix(path, ".go") {
				filesToFormat = append(filesToFormat, path)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("error walking for go files: %w", err)
		}
	}

	if len(filesToFormat) == 0 {
		return nil
	}

	log.Info("Running gofmt", "files", len(filesToFormat))

	// Chunk files to avoid argument length limits
	chunkSize := 100
	for i := 0; i < len(filesToFormat); i += chunkSize {
		end := i + chunkSize
		if end > len(filesToFormat) {
			end = len(filesToFormat)
		}
		chunk := filesToFormat[i:end]

		args := append([]string{"-w"}, chunk...)
		cmd := exec.CommandContext(ctx, "gofmt", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("gofmt failed: %w", err)
		}
	}
	return nil
}

func runGoVet(ctx context.Context, repoRoot string) error {
	log := klog.FromContext(ctx)
	log.Info("Running go vet")

	var goModDirs []string
	err := filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "vendor" || info.Name() == ".git" {
				return filepath.SkipDir
			}
		}
		if !info.IsDir() && info.Name() == "go.mod" {
			goModDirs = append(goModDirs, filepath.Dir(path))
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("error walking for go.mod files: %w", err)
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
