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
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
)

func TestManager_Load_CorruptDoneBinTreatAsEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	// Write corrupt binary to done.bin
	if err := os.WriteFile(filepath.Join(tmpDir, "done.bin"), []byte("corrupt binary data"), 0644); err != nil {
		t.Fatal(err)
	}

	m := &Manager{
		dir:  tmpDir,
		done: NewDoneIndex(),
		log:  scoped.NewLogger(slog.DiscardHandler),
	}

	// Corrupt done.bin must be logged at V(2) and treated as empty without returning an error
	err := m.load()
	if err != nil {
		t.Fatalf("expected nil error from load() on corrupt done.bin, got: %v", err)
	}
	if m.done == nil || len(m.done.records) != 0 {
		t.Errorf("expected empty done cache after corrupt done.bin")
	}
}

func TestManager_Load_NotExist(t *testing.T) {
	tmpDir := t.TempDir()
	m := &Manager{
		dir:  tmpDir,
		done: NewDoneIndex(),
		log:  scoped.NewLogger(slog.DiscardHandler),
	}

	err := m.load()
	if err != nil {
		t.Fatalf("expected nil error when cache files do not exist, got: %v", err)
	}
}

func TestManager_DoneCache(t *testing.T) {
	tmpDir := t.TempDir()

	// Write legacy files to verify they are deleted
	legacyMeta := filepath.Join(tmpDir, "metadata.json")
	legacyDone := filepath.Join(tmpDir, "done.json")
	legacyGofmt := filepath.Join(tmpDir, "gofmt.json")
	if err := os.WriteFile(legacyMeta, []byte(`{"legacy": true}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyDone, []byte(`{"legacy": true}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyGofmt, []byte(`{"legacy": true}`), 0644); err != nil {
		t.Fatal(err)
	}

	m, err := NewManagerWithDir(t.Context(), tmpDir)
	if err != nil {
		t.Fatalf("NewManagerWithDir failed: %v", err)
	}

	// Legacy files should have been removed on load
	if _, err := os.Stat(legacyMeta); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected legacy metadata.json to be deleted after load, err: %v", err)
	}
	if _, err := os.Stat(legacyDone); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected legacy done.json to be deleted after load, err: %v", err)
	}
	if _, err := os.Stat(legacyGofmt); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected legacy gofmt.json to be deleted after load, err: %v", err)
	}

	gofmtHash := makeHexHash(0xab, 1)
	tidyHash := makeHexHash(0xcd, 2)
	otherHash := makeHexHash(0xef, 3)

	if m.IsDone(StepGofmt, gofmtHash) {
		t.Errorf("expected IsDone(StepGofmt) to be false initially")
	}
	if m.IsDone(StepTidy, tidyHash) {
		t.Errorf("expected IsDone(StepTidy) to be false initially")
	}

	m.MarkDone(StepGofmt, gofmtHash)
	m.MarkDone(StepTidy, tidyHash)

	if !m.IsDone(StepGofmt, gofmtHash) {
		t.Errorf("expected IsDone(StepGofmt) to be true after MarkDone")
	}
	if !m.IsDone(StepTidy, tidyHash) {
		t.Errorf("expected IsDone(StepTidy) to be true after MarkDone")
	}

	if err := m.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify done.bin exists
	doneBinPath := filepath.Join(tmpDir, "done.bin")
	if _, err := os.Stat(doneBinPath); err != nil {
		t.Fatalf("expected done.bin to exist after Save: %v", err)
	}

	// Reload in a new manager
	m2, err := NewManagerWithDir(t.Context(), tmpDir)
	if err != nil {
		t.Fatalf("NewManagerWithDir reload failed: %v", err)
	}
	if !m2.IsDone(StepGofmt, gofmtHash) {
		t.Errorf("expected IsDone(StepGofmt) to persist after reload")
	}
	if !m2.IsDone(StepTidy, tidyHash) {
		t.Errorf("expected IsDone(StepTidy) to persist after reload")
	}
	if m2.IsDone(StepGofmt, otherHash) {
		t.Errorf("expected IsDone for other hash to be false")
	}
}
