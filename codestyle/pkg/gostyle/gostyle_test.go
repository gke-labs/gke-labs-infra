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

package gostyle

import (
	"context"
	"os"
	"testing"
)

func TestRun_NoConfig(t *testing.T) {
	// Create a temporary directory for the mock repo
	tmpDir, err := os.MkdirTemp("", "gostyle-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()

	// Run should succeed (do nothing) when no config exists
	err = Run(ctx, tmpDir, nil)
	if err != nil {
		t.Errorf("Expected success from Run (no config), got error: %v", err)
	}
}
