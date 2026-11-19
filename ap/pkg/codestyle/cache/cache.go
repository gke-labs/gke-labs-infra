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
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
)

type Manager struct {
	dir  string
	done *DoneIndex
	log  *slog.Logger
	mu   sync.Mutex
}

func NewManager(ctx context.Context) (*Manager, error) {
	cacheDir, err := fileutils.CacheDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(cacheDir, "codestyle")
	return NewManagerWithDir(ctx, dir)
}

func NewManagerWithDir(ctx context.Context, dir string) (*Manager, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	m := &Manager{
		dir:  dir,
		done: NewDoneIndex(),
		log:  scoped.Log(ctx),
	}
	if err := m.load(); err != nil {
		m.log.Warn("falling back to empty codestyle cache", "error", err)
	}
	return m, nil
}

func (m *Manager) removeLegacyFile(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		m.log.Log(context.Background(), scoped.LevelVerbose2, "failed to remove legacy cache file", "path", path, "error", err)
	}
}

func (m *Manager) load() error {
	var errs []error

	// Ignore and delete legacy JSON caches
	m.removeLegacyFile(filepath.Join(m.dir, "metadata.json"))
	m.removeLegacyFile(filepath.Join(m.dir, "done.json"))
	m.removeLegacyFile(filepath.Join(m.dir, "gofmt.json"))

	doneBinPath := filepath.Join(m.dir, "done.bin")
	if data, err := os.ReadFile(doneBinPath); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			m.log.Log(context.Background(), scoped.LevelVerbose2, "failed to read done cache, starting with empty cache", "path", doneBinPath, "error", err)
		}
		m.done = NewDoneIndex()
	} else if idx, err := LoadDone(data); err != nil {
		m.log.Log(context.Background(), scoped.LevelVerbose2, "invalid done cache, starting with empty cache", "path", doneBinPath, "error", err)
		m.done = NewDoneIndex()
	} else {
		m.done = idx
	}

	return errors.Join(errs...)
}

func (m *Manager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Clean up legacy JSON caches
	m.removeLegacyFile(filepath.Join(m.dir, "metadata.json"))
	m.removeLegacyFile(filepath.Join(m.dir, "done.json"))
	m.removeLegacyFile(filepath.Join(m.dir, "gofmt.json"))

	doneBinPath := filepath.Join(m.dir, "done.bin")
	if err := m.done.Save(doneBinPath); err != nil {
		return err
	}
	return nil
}

func (m *Manager) IsDone(step Step, hash string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done == nil {
		return false
	}
	return m.done.IsDone(step, hash)
}

func (m *Manager) MarkDone(step Step, hash string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done == nil {
		m.done = NewDoneIndex()
	}
	if err := m.done.MarkDone(step, hash); err != nil {
		m.log.Log(context.Background(), scoped.LevelVerbose2, "failed to mark done", "step", int(step), "error", err)
	}
}
