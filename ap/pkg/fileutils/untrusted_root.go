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
	"io/fs"
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

// WalkStagedOutputs walks outputs inside stagingDirectory using an os.Root on stagingDirectory.
// For each regular file found, it calls walkFn with the clean relative path, file info, and staging root.
// Symlinks, devices, sockets, and paths escaping the root or containing .git
// are rejected with an error naming the path.
func WalkStagedOutputs(stagingDirectory string, outputs []string, walkFn func(path string, info os.FileInfo, stagingRoot *os.Root) error) (returnError error) {
	stagingRoot, err := os.OpenRoot(stagingDirectory)
	if err != nil {
		return fmt.Errorf("opening staging root %s: %w", stagingDirectory, err)
	}
	defer func() {
		if closeErr := stagingRoot.Close(); closeErr != nil && returnError == nil {
			returnError = closeErr
		}
	}()

	for _, outputEntry := range outputs {
		cleanEntry := filepath.Clean(outputEntry)
		if cleanEntry == ".." || strings.HasPrefix(cleanEntry, ".."+string(filepath.Separator)) || filepath.IsAbs(outputEntry) {
			return fmt.Errorf("%s: escapes staging root", outputEntry)
		}
		if hasGitComponent(cleanEntry) {
			return fmt.Errorf("%s: is under .git", outputEntry)
		}

		entryInfo, err := stagingRoot.Lstat(cleanEntry)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("%s: %w", cleanEntry, err)
		}

		if entryInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: is a symlink", cleanEntry)
		}
		if !entryInfo.Mode().IsRegular() && !entryInfo.IsDir() {
			return fmt.Errorf("%s: is not a regular file or directory", cleanEntry)
		}

		if entryInfo.IsDir() {
			walkErr := fs.WalkDir(stagingRoot.FS(), cleanEntry, func(path string, dirEntry fs.DirEntry, walkError error) error {
				if walkError != nil {
					return walkError
				}
				if path == "." {
					return nil
				}
				info, err := stagingRoot.Lstat(path)
				if err != nil {
					return fmt.Errorf("%s: %w", path, err)
				}
				if info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("%s: is a symlink", path)
				}
				if info.IsDir() {
					return nil
				}
				if !info.Mode().IsRegular() {
					return fmt.Errorf("%s: is not a regular file or directory", path)
				}
				return walkFn(path, info, stagingRoot)
			})
			if walkErr != nil {
				return walkErr
			}
		} else {
			if err := walkFn(cleanEntry, entryInfo, stagingRoot); err != nil {
				return err
			}
		}
	}

	return nil
}

// CopyStagedOutputs copies files matching outputs from stagingDirectory into the root.
// It opens an os.Root on stagingDirectory and reads files only through that root.
// Symlinks, devices, sockets, and paths escaping the root or containing .git
// are rejected with an error naming the path.
func (r *UntrustedRoot) CopyStagedOutputs(stagingDirectory string, outputs []string) error {
	return WalkStagedOutputs(stagingDirectory, outputs, func(path string, info os.FileInfo, stagingRoot *os.Root) error {
		content, err := stagingRoot.ReadFile(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		return r.WriteFile(path, content, info.Mode().Perm())
	})
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
