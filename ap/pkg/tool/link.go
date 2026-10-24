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

package tool

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
)

// LinkOptions holds options for Link.
type LinkOptions struct {
	TargetDir  string
	Executable string
	Stdout     io.Writer
}

// DefaultLinkDir returns the default directory for linking tool symlinks (AP_CACHE_DIR/bin).
func DefaultLinkDir() (string, error) {
	cacheRoot, err := fileutils.CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheRoot, "bin"), nil
}

// Link creates or refreshes symlinks for all registered tools pointing at the ap executable.
func Link(opt LinkOptions) ([]string, error) {
	if runtime.GOOS == "windows" {
		return nil, fmt.Errorf("ap tool link is not supported on Windows")
	}

	targetDir := opt.TargetDir
	if targetDir == "" {
		var err error
		targetDir, err = DefaultLinkDir()
		if err != nil {
			return nil, err
		}
	}

	targetDir, err := filepath.Abs(targetDir)
	if err != nil {
		return nil, fmt.Errorf("failed to determine absolute path for %s: %w", targetDir, err)
	}

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory %s: %w", targetDir, err)
	}

	execPath := opt.Executable
	if execPath == "" {
		execPath = os.Getenv("AP_EXECUTABLE")
	}
	if execPath == "" {
		var err error
		execPath, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("failed to determine executable path: %w", err)
		}
	}

	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve symlink for executable %s: %w", execPath, err)
	}
	execPath, err = filepath.Abs(execPath)
	if err != nil {
		return nil, fmt.Errorf("failed to determine absolute path for executable: %w", err)
	}

	stdout := opt.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}

	var created []string
	for _, t := range All() {
		linkPath := filepath.Join(targetDir, t.Name)

		if err := os.Remove(linkPath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to remove existing link %s: %w", linkPath, err)
		}

		if err := os.Symlink(execPath, linkPath); err != nil {
			return nil, fmt.Errorf("failed to create symlink %s -> %s: %w", linkPath, execPath, err)
		}

		if _, err := fmt.Fprintf(stdout, "%s -> %s\n", linkPath, execPath); err != nil {
			return nil, fmt.Errorf("failed to write link output for %s: %w", linkPath, err)
		}
		created = append(created, linkPath)
	}

	return created, nil
}
