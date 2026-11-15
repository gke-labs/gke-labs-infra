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

package fileutils

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// UntrustedRoot writes files produced by a sandbox or plugin under a directory.
// Paths are relative; a path that leaves the directory (by ".." or through a
// symlink) or that has a ".git" component is rejected.
type UntrustedRoot struct {
	root *os.Root
}

// OpenUntrustedRoot opens an UntrustedRoot for writing files under directory.
func OpenUntrustedRoot(directory string) (*UntrustedRoot, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("failed to open root %s: %w", directory, err)
	}
	return &UntrustedRoot{root: root}, nil
}

// Close closes the underlying root directory.
func (r *UntrustedRoot) Close() error {
	return r.root.Close()
}

// WriteFile writes content to relativePath under the directory, creating parent
// directories as needed. Paths that escape the directory (by ".." or through a
// symlink) or that have a ".git" component are rejected.
func (r *UntrustedRoot) WriteFile(relativePath string, content []byte, permissions os.FileMode) error {
	if hasGitComponent(relativePath) {
		return fmt.Errorf("%s: is under .git", relativePath)
	}

	directory := filepath.Dir(relativePath)
	if directory != "." {
		if err := r.root.MkdirAll(directory, 0755); err != nil {
			return fmt.Errorf("%s: %w", relativePath, err)
		}
	}

	if err := r.root.WriteFile(relativePath, content, permissions); err != nil {
		return fmt.Errorf("%s: %w", relativePath, err)
	}

	return nil
}

// hasGitComponent reports whether any path component is ".git".
func hasGitComponent(path string) bool {
	for _, component := range strings.Split(filepath.ToSlash(filepath.Clean(path)), "/") {
		if strings.EqualFold(component, ".git") {
			return true
		}
	}
	return false
}
