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

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sandbox/api"
)

func TestServerWriteRead(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ap-sandbox-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	s := &server{root: tmpDir}
	ctx := context.Background()

	// Test WriteFile
	testPath := "test.txt"
	testContent := []byte("hello world")
	_, err = s.WriteFile(ctx, &api.WriteFileRequest{
		Path:    testPath,
		Content: testContent,
	})
	if err != nil {
		t.Errorf("WriteFile failed: %v", err)
	}

	// Verify file exists
	fullPath := filepath.Join(tmpDir, testPath)
	content, err := os.ReadFile(fullPath)
	if err != nil {
		t.Errorf("Failed to read file from disk: %v", err)
	}
	if string(content) != string(testContent) {
		t.Errorf("Content mismatch: got %q, want %q", string(content), string(testContent))
	}

	// Test ReadFile
	resp, err := s.ReadFile(ctx, &api.ReadFileRequest{
		Path: testPath,
	})
	if err != nil {
		t.Errorf("ReadFile failed: %v", err)
	}
	if string(resp.Content) != string(testContent) {
		t.Errorf("Content mismatch from ReadFile: got %q, want %q", string(resp.Content), string(testContent))
	}
}
