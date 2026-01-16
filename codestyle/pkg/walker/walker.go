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

// Filter is a function that returns true if the file should be included.
type Filter func(path string, info os.FileInfo) bool

// Walk walks the directory tree rooted at root and returns a list of files.
// It skips paths matched by the ignore list.
// If filter is provided, it only returns files for which filter returns true.
func Walk(root string, ignore *IgnoreList, filter Filter) ([]string, error) {
	var files []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}

		if ignore != nil && ignore.ShouldIgnore(relPath, info.IsDir()) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.IsDir() {
			return nil
		}

		if filter != nil && !filter(path, info) {
			return nil
		}

		files = append(files, path)
		return nil
	})

	return files, err
}
