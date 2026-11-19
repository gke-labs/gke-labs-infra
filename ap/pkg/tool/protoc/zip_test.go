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

package protoc

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractZip_Success(t *testing.T) {
	tempDir := t.TempDir()
	zipPath := filepath.Join(tempDir, "test.zip")
	destDir := filepath.Join(tempDir, "dest")

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	// Add directory
	_, err := zw.Create("sub/")
	if err != nil {
		t.Fatal(err)
	}

	// Add file inside directory
	f, err := zw.Create("sub/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("hello world")); err != nil {
		t.Fatal(err)
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(zipPath, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	if err := extractZip(t.Context(), zipPath, destDir); err != nil {
		t.Fatalf("extractZip failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(destDir, "sub", "hello.txt"))
	if err != nil {
		t.Fatalf("failed to read extracted file: %v", err)
	}
	if string(content) != "hello world" {
		t.Errorf("got %q, want %q", string(content), "hello world")
	}
}

func TestExtractZip_IllegalPath(t *testing.T) {
	tempDir := t.TempDir()
	zipPath := filepath.Join(tempDir, "traversal.zip")
	destDir := filepath.Join(tempDir, "dest")

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	f, err := zw.Create("../outside.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("evil")); err != nil {
		t.Fatal(err)
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(zipPath, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	err = extractZip(t.Context(), zipPath, destDir)
	if err == nil {
		t.Fatal("expected error on illegal path in zip, got nil")
	}
	if !strings.Contains(err.Error(), "illegal relative file path") {
		t.Errorf("expected error to mention illegal path, got: %v", err)
	}
}
