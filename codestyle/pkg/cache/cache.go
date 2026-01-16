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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
)

type Caches struct {
	Metadata map[string]*FileMetadata `json:"metadata"`
	Gofmt    map[string]bool          `json:"gofmt"`
}

type Manager struct {
	dir    string
	caches *Caches
	mu     sync.Mutex
}

func NewManager() (*Manager, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(cacheDir, "ap", "codestyle")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	m := &Manager{
		dir: dir,
		caches: &Caches{
			Metadata: make(map[string]*FileMetadata),
			Gofmt:    make(map[string]bool),
		},
	}
	// Ignore errors on load (start fresh)
	_ = m.load()
	return m, nil
}

func (m *Manager) load() error {
	metaPath := filepath.Join(m.dir, "metadata.json")
	if data, err := os.ReadFile(metaPath); err == nil {
		var meta map[string]*FileMetadata
		if err := json.Unmarshal(data, &meta); err == nil {
			m.caches.Metadata = meta
		}
	}

	gofmtPath := filepath.Join(m.dir, "gofmt.json")
	if data, err := os.ReadFile(gofmtPath); err == nil {
		var gofmt map[string]bool
		if err := json.Unmarshal(data, &gofmt); err == nil {
			m.caches.Gofmt = gofmt
		}
	}
	return nil
}

func (m *Manager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	metaPath := filepath.Join(m.dir, "metadata.json")
	metaData, err := json.MarshalIndent(m.caches.Metadata, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(metaPath, metaData, 0644); err != nil {
		return err
	}

	gofmtPath := filepath.Join(m.dir, "gofmt.json")
	gofmtData, err := json.MarshalIndent(m.caches.Gofmt, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(gofmtPath, gofmtData, 0644); err != nil {
		return err
	}
	return nil
}

// GetOrUpdateMetadata returns the FileMetadata with Hash populated.
// If the file on disk matches the cached metadata (Size, Mtime, Inode), the cached Hash is used.
// Otherwise, the file is read and hashed, and the cache is updated.
func (m *Manager) GetOrUpdateMetadata(path string) (*FileMetadata, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Get current stat
	current, err := GetMetadata(path)
	if err != nil {
		return nil, err
	}

	cached, ok := m.caches.Metadata[path]
	if ok && cached.Size == current.Size && cached.Mtime == current.Mtime && cached.Inode == current.Inode {
		return cached, nil
	}

	// Hash the file
	hash, err := hashFile(path)
	if err != nil {
		return nil, err
	}
	current.Hash = hash
	m.caches.Metadata[path] = current
	return current, nil
}

func (m *Manager) IsGofmtDone(hash string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.caches.Gofmt[hash]
}

func (m *Manager) MarkGofmtDone(hash string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.caches.Gofmt[hash] = true
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
