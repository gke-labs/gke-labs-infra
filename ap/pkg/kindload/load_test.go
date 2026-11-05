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

package kindload

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadImages_RequiresKindBin(t *testing.T) {
	err := LoadImages(t.Context(), "", "ap-cluster", []string{"test-image:latest"}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected error when kindBin is empty, got nil")
	}
	if !strings.Contains(err.Error(), "kindBin is required") {
		t.Fatalf("expected error to contain 'kindBin is required', got: %v", err)
	}

	err = LoadImages(t.Context(), "", "ap-cluster", nil, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected error when kindBin is empty and images is nil, got nil")
	}
}

func TestLoadImages_EmptyImages(t *testing.T) {
	err := LoadImages(t.Context(), "kind", "ap-cluster", nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("expected nil when images is empty, got: %v", err)
	}
	err = LoadImages(t.Context(), "kind", "ap-cluster", []string{}, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("expected nil when images is empty, got: %v", err)
	}
}

func TestLoadImages_CommandFails(t *testing.T) {
	tempDir := t.TempDir()
	fakeBin := filepath.Join(tempDir, "kind")
	script := "#!/bin/sh\nexit 1\n"
	if err := os.WriteFile(fakeBin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	err := LoadImages(t.Context(), fakeBin, "test-cluster", []string{"test-image:latest"}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected error when kind fails, got nil")
	}
	if !strings.Contains(err.Error(), "failed to load image(s) into kind cluster \"test-cluster\"") {
		t.Errorf("expected error to mention cluster, got: %v", err)
	}
}
