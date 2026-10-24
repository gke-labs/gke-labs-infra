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

package gotool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"golang.org/x/mod/modfile"
)

// ResolveVersion determines which Go toolchain version to use based on the following precedence order:
//  1. The `toolchain` line of the nearest `go.mod` file when run inside a module.
//  2. The `go` line of the nearest `go.mod` file when run inside a module.
//  3. The `toolchain` setting in `.ap/go.yaml` if configured.
//  4. The newest stable release in the embedded versions table.
func ResolveVersion(dir, repoRoot string) (string, error) {
	startDir := dir
	if startDir == "" {
		var err error
		startDir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get current working directory: %w", err)
		}
	}

	// 1 & 2: Nearest go.mod inside a module
	goModPath, err := FindNearestGoMod(startDir)
	if err != nil {
		return "", err
	}
	if goModPath != "" {
		toolchainVer, goVer, err := ParseGoMod(goModPath)
		if err != nil {
			return "", fmt.Errorf("failed to parse %s: %w", goModPath, err)
		}
		if toolchainVer != "" {
			return NormalizeVersion(toolchainVer), nil
		}
		if goVer != "" {
			return NormalizeVersion(goVer), nil
		}
	}

	// 3: .ap/go.yaml toolchain setting
	cfg, err := loadGoConfig(startDir, repoRoot)
	if err != nil {
		return "", err
	}
	if cfg != nil && strings.TrimSpace(cfg.Toolchain) != "" {
		return NormalizeVersion(cfg.Toolchain), nil
	}

	// 4: Newest table entry
	latest := LatestGo()
	if latest == nil {
		return "", fmt.Errorf("no Go releases found in versions.yaml")
	}
	return latest.Version, nil
}

// FindNearestGoMod searches upwards from startDir for the nearest go.mod file.
// Returns the absolute path to go.mod, or "" if not found.
func FindNearestGoMod(startDir string) (string, error) {
	absDir, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("failed to resolve absolute path for %s: %w", startDir, err)
	}

	dir := absDir
	for {
		modPath := filepath.Join(dir, "go.mod")
		fi, err := os.Stat(modPath)
		if err == nil && !fi.IsDir() {
			return modPath, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("failed to stat %s: %w", modPath, err)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", nil
}

// ParseGoMod reads a go.mod file and returns its toolchain and go version directives using modfile.ParseLax.
func ParseGoMod(path string) (toolchainVer, goVer string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}

	f, err := modfile.ParseLax(path, data, nil)
	if err != nil {
		return "", "", fmt.Errorf("error parsing %s: %w", path, err)
	}

	if f.Toolchain != nil {
		toolchainVer = f.Toolchain.Name
	} else if f.Syntax != nil {
		for _, stmt := range f.Syntax.Stmt {
			if line, ok := stmt.(*modfile.Line); ok && len(line.Token) >= 2 {
				if line.Token[0] == "toolchain" {
					toolchainVer = line.Token[1]
					break
				}
			}
		}
	}
	if f.Go != nil {
		goVer = f.Go.Version
	}

	return toolchainVer, goVer, nil
}

func loadGoConfig(startDir, repoRoot string) (*config.Config, error) {
	// Check startDir first
	if startDir != "" {
		cfg, err := config.Load(startDir)
		if err != nil {
			return nil, err
		}
		if cfg != nil && strings.TrimSpace(cfg.Toolchain) != "" {
			return cfg, nil
		}

		// Walk up from startDir towards repoRoot or root
		dir := filepath.Dir(startDir)
		for dir != "" && dir != startDir {
			cfg, err := config.Load(dir)
			if err != nil {
				return nil, err
			}
			if cfg != nil && strings.TrimSpace(cfg.Toolchain) != "" {
				return cfg, nil
			}
			if repoRoot != "" && dir == repoRoot {
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	// Finally check repoRoot
	if repoRoot != "" && repoRoot != startDir {
		cfg, err := config.Load(repoRoot)
		if err != nil {
			return nil, err
		}
		if cfg != nil {
			return cfg, nil
		}
	}

	return nil, nil
}
