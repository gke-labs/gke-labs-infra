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

package cache

import (
	"os"
	"syscall"
)

// FileMetadata represents the "Fingerprint" of a file at a specific point in time.
type FileMetadata struct {
	Path  string
	Size  int64
	Mtime int64 // Nanoseconds
	Inode uint64
	Hash  string
}

// GetMetadata retrieves the stat-based fingerprint of a file.
// Note: This does NOT compute the Hash. The Hash must be populated separately.
func GetMetadata(path string) (*FileMetadata, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}

	// Access underlying syscall data for Inode and Nano-precision Mtime
	stat := fi.Sys().(*syscall.Stat_t)

	return &FileMetadata{
		Path:  path,
		Size:  fi.Size(),
		Mtime: stat.Mtim.Nano(), // Linux/Unix specific
		Inode: stat.Ino,
	}, nil
}
