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
	"fmt"
	"os"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
)

// File represents a file in the file system.
type File struct {
	Path    string
	Info    os.FileInfo
	RelPath string
}

// FileView represents a view of a directory tree, with ignore patterns.
type FileView struct {
	Tree   *sourcetree.SourceTree
	Dir    string
	Ignore *IgnoreList
}

// NewFileView creates a new FileView backed by the sourcetree snapshot.
func NewFileView(r *sourcetree.SourceTree, dir string, ignorePatterns []string) (*FileView, error) {
	ignoreList, err := NewIgnoreList(ignorePatterns)
	if err != nil {
		return nil, fmt.Errorf("building ignore list: %w", err)
	}
	return &FileView{
		Tree:   r,
		Dir:    dir,
		Ignore: ignoreList,
	}, nil
}

// Walk walks the directory tree and calls callback for each file.
func (v *FileView) Walk(callback func(File) error) error {
	return v.Tree.WalkFileView(v.Dir, v.Ignore, func(f *sourcetree.File, relPath string) error {
		return callback(File{
			Path:    f.Path,
			Info:    f.Info(),
			RelPath: relPath,
		})
	})
}
