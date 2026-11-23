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

package images

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseBuildKitProgress(t *testing.T) {
	tests := []struct {
		name               string
		input              string
		wantCachedSteps    map[string]bool
		wantStepDurations  map[string]float64
		wantExportDuration float64
	}{
		{
			name: "mixed cached and built steps",
			input: strings.TrimSpace(`
#0 building with "default" instance using docker driver
#1 [internal] load build definition from Dockerfile
#1 DONE 0.1s
#2 [builder 1/2] FROM python:3.12-slim
#2 DONE 0.5s
#3 [builder 2/2] RUN pip install torch
#3 CACHED
#4 [stage-1 2/2] COPY --from=builder /opt/venv /opt/venv
#4 DONE 2.3s
#5 exporting cache to client directory
#5 DONE 12.4s
`),
			wantCachedSteps: map[string]bool{
				"[builder 2/2] RUN pip install torch": true,
			},
			wantStepDurations: map[string]float64{
				"[stage-1 2/2] COPY --from=builder /opt/venv /opt/venv": 2.3,
			},
			wantExportDuration: 12.4,
		},
		{
			name: "all cached steps",
			input: strings.TrimSpace(`
#1 [builder 1/1] WORKDIR /workspace
#1 CACHED
#2 [builder 2/2] RUN npm ci
#2 CACHED
`),
			wantCachedSteps: map[string]bool{
				"[builder 1/1] WORKDIR /workspace": true,
				"[builder 2/2] RUN npm ci":         true,
			},
			wantStepDurations:  map[string]float64{},
			wantExportDuration: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseBuildKitProgress(strings.NewReader(tt.input))
			for step, wantCached := range tt.wantCachedSteps {
				if got.cachedSteps[step] != wantCached {
					t.Errorf("step %q cached = %v, want %v", step, got.cachedSteps[step], wantCached)
				}
			}
			for step, wantDuration := range tt.wantStepDurations {
				if got.stepDurations[step] != wantDuration {
					t.Errorf("step %q duration = %v, want %v", step, got.stepDurations[step], wantDuration)
				}
			}
			if got.exportDuration != tt.wantExportDuration {
				t.Errorf("exportDuration = %v, want %v", got.exportDuration, tt.wantExportDuration)
			}
		})
	}
}

func TestLayerCache_SavedVsCost_Evaluation(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_EVENT_NAME", "push")
	t.Setenv("RUNNER_TEMP", tempDir)

	dockerfilePath := filepath.Join(tempDir, "Dockerfile")
	if err := os.WriteFile(dockerfilePath, []byte("FROM alpine\nRUN echo hello\n"), 0644); err != nil {
		t.Fatal(err)
	}

	finalDir := filepath.Join(tempDir, "ap-layer-cache", "test-image")

	// 1. Cold build: exports metadata and keeps
	cache := setupLayerCache(t.Context(), "test-image", dockerfilePath)
	if cache.exportArg == "" {
		t.Fatal("expected exportArg set on push")
	}
	coldLog := strings.NewReader("#1 [1/1] RUN echo hello\n#1 DONE 2.0s\n#2 exporting cache to client directory\n#2 DONE 8.0s\n")
	cache.evaluate(t.Context(), "test-image", coldLog)

	if _, err := os.Stat(filepath.Join(finalDir, "metadata.json")); err != nil {
		t.Fatalf("expected metadata.json saved after cold run: %v", err)
	}

	// 2. Warm build with saved <= cost (saved 2s <= cost 13s: export 8s + 5s restore share): drops cache
	if err := os.WriteFile(filepath.Join(finalDir, "index.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	warmCache := setupLayerCache(t.Context(), "test-image", dockerfilePath)
	if warmCache.importArg == "" {
		t.Fatal("expected importArg set on warm run")
	}
	warmLog := strings.NewReader("#1 [1/1] RUN echo hello\n#1 CACHED\n")
	warmCache.evaluate(t.Context(), "test-image", warmLog)

	// After drop: index.json removed, metadata.json has dropped=true
	if _, err := os.Stat(filepath.Join(finalDir, "index.json")); !os.IsNotExist(err) {
		t.Errorf("expected index.json to be deleted when dropped")
	}
	meta, ok := readImageCacheMetadata(filepath.Join(finalDir, "metadata.json"))
	if !ok || !meta.Dropped {
		t.Errorf("expected metadata.Dropped=true, got %+v, found=%v", meta, ok)
	}

	// 3. Next run on same Dockerfile: judged not worth it, passes neither import nor export
	skipCache := setupLayerCache(t.Context(), "test-image", dockerfilePath)
	if skipCache.importArg != "" || skipCache.exportArg != "" {
		t.Errorf("expected no cache flags for dropped image, got %+v", skipCache)
	}

	// 4. Dockerfile changed: retries
	if err := os.WriteFile(dockerfilePath, []byte("FROM alpine\nRUN echo hello v2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	retryCache := setupLayerCache(t.Context(), "test-image", dockerfilePath)
	if retryCache.exportArg == "" {
		t.Errorf("expected exportArg re-enabled after Dockerfile changed")
	}
}
