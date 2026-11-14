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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"k8s.io/klog/v2"
)

// FileExists reports whether the named file or directory exists.
// It returns false, nil only when errors.Is(err, fs.ErrNotExist).
// All other errors are returned.
func FileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// CacheDir returns the root cache directory for ap tools.
func CacheDir() (string, error) {
	if env := os.Getenv("AP_CACHE_DIR"); env != "" {
		return env, nil
	}
	userCache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("failed to determine user cache dir: %w", err)
	}
	return filepath.Join(userCache, "ap"), nil
}

// TempFile is a temporary file created next to its final destination.
// Rename moves it into place; Cleanup removes it unless it was renamed.
type TempFile struct {
	file    *os.File
	name    string
	dest    string
	renamed bool
	closed  bool
}

// CreateTempFile creates a temporary file in the directory of dest.
func CreateTempFile(dest string) (*TempFile, error) {
	destDir := filepath.Dir(dest)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory for %s: %w", dest, err)
	}

	pattern := fmt.Sprintf(".%s-*.tmp", filepath.Base(dest))
	file, err := os.CreateTemp(destDir, pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary file for %s: %w", dest, err)
	}

	return &TempFile{
		file: file,
		name: file.Name(),
		dest: dest,
	}, nil
}

// Write writes p to the underlying temporary file.
func (t *TempFile) Write(p []byte) (int, error) {
	return t.file.Write(p)
}

// Rename sets the permissions, closes the file, and moves it to dest.
// If successful, it marks the TempFile as renamed so that Cleanup is a no-op.
func (t *TempFile) Rename(perm os.FileMode) error {
	if t.renamed {
		return fmt.Errorf("temporary file for %s already renamed", t.dest)
	}
	if t.closed {
		return fmt.Errorf("temporary file for %s is already closed", t.dest)
	}
	if err := t.file.Chmod(perm); err != nil {
		return fmt.Errorf("failed to chmod temporary file for %s: %w", t.dest, err)
	}
	t.closed = true
	if err := t.file.Close(); err != nil {
		return fmt.Errorf("failed to close temporary file for %s: %w", t.dest, err)
	}
	if err := os.Rename(t.name, t.dest); err != nil {
		return fmt.Errorf("failed to move temporary file to %s: %w", t.dest, err)
	}
	t.renamed = true
	return nil
}

// Cleanup closes the temporary file if still open and removes it unless it was renamed.
// Failures to close or remove are logged. Cleanup is safe to call after Rename.
func (t *TempFile) Cleanup() {
	if t.renamed {
		return
	}
	if !t.closed {
		t.closed = true
		if err := t.file.Close(); err != nil {
			klog.V(2).Infof("failed to close temporary file %s: %v", t.name, err)
		}
	}
	if err := os.Remove(t.name); err != nil && !os.IsNotExist(err) {
		klog.V(2).Infof("failed to remove temporary file %s: %v", t.name, err)
	}
}

// WriteFileAtomic writes data to path atomically: write to a temp file in the target's
// directory, set the target mode, close (error checked), then rename over the target.
// Any failure removes the temporary file.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tempFile, err := CreateTempFile(path)
	if err != nil {
		return err
	}
	defer tempFile.Cleanup()

	if _, err := tempFile.Write(data); err != nil {
		return err
	}
	return tempFile.Rename(perm)
}

// CopyFile copies a file from src to dst atomically via a temporary file, preserving permissions and mtime.
func CopyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := in.Close(); closeErr != nil {
			klog.V(2).Infof("failed to close input file %s: %v", src, closeErr)
		}
	}()

	srcFi, err := in.Stat()
	if err != nil {
		return err
	}

	tempFile, err := CreateTempFile(dst)
	if err != nil {
		return err
	}
	defer tempFile.Cleanup()

	if _, err := io.Copy(tempFile, in); err != nil {
		return err
	}

	if err := tempFile.Rename(perm); err != nil {
		return err
	}

	if chtimesErr := os.Chtimes(dst, srcFi.ModTime(), srcFi.ModTime()); chtimesErr != nil {
		klog.V(2).Infof("failed to set mtime on %s: %v", dst, chtimesErr)
	}
	return nil
}

// PrependPath returns a copy of env with dir prepended to the PATH environment variable.
func PrependPath(env []string, dir string) []string {
	if dir == "" {
		return env
	}
	pathKey := "PATH"
	var existingPath string
	found := false
	var result []string
	for _, entry := range env {
		if len(entry) >= 5 && strings.EqualFold(entry[:5], "PATH=") {
			pathKey = entry[:4]
			existingPath = entry[5:]
			found = true
			continue
		}
		result = append(result, entry)
	}

	newPath := dir
	if found && existingPath != "" {
		newPath = dir + string(os.PathListSeparator) + existingPath
	}
	result = append(result, pathKey+"="+newPath)
	return result
}

// IsExecutable reports whether the file at path exists and is an executable file.
func IsExecutable(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return fi.Mode().Perm()&0111 != 0
}
