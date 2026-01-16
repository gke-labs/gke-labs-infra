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

package walker

import (
	"os"
	"path/filepath"
)

// File represents a file in the file system.
type File struct {
	Path    string
	Info    os.FileInfo
	RelPath string
}

// FileView represents a view of a directory tree, with ignore patterns.
type FileView struct {
	Dir    string
	Ignore *IgnoreList
}

// NewFileView creates a new FileView.
func NewFileView(dir string, ignorePatterns []string) *FileView {
	return &FileView{
		Dir:    dir,
		Ignore: NewIgnoreList(ignorePatterns),
	}
}

// Walk walks the directory tree and calls callback for each file.
func (v *FileView) Walk(callback func(File) error) error {
	return filepath.Walk(v.Dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(v.Dir, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}

		if v.Ignore != nil && v.Ignore.ShouldIgnore(relPath, info.IsDir()) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.IsDir() {
			return nil
		}

		return callback(File{
			Path:    path,
			Info:    info,
			RelPath: relPath,
		})
	})
}
