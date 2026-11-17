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

package plugins_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

func setupTestRepo(t *testing.T) string {
	t.Helper()
	repoRoot := t.TempDir()

	files := map[string]string{
		".gitignore":               "ignored.txt\n*.tmp\n",
		"ignored.txt":              "this is ignored\n",
		"images/plugin/Dockerfile": "FROM alpine\nRUN echo hello\n",
		"pkg/app/main.go":          "package main\n\nfunc main() {}\n",
		"docs/readme.md":           "# Demo\n",
	}

	for relPath, content := range files {
		absPath := filepath.Join(repoRoot, relPath)
		if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	return repoRoot
}

func TestDockerfilePluginCacheKey_Stable(t *testing.T) {
	repoRoot := setupTestRepo(t)
	sourcetree.ResetCache()
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	dockerfilePath := "images/plugin/Dockerfile"
	key1, err := plugins.DockerfilePluginCacheKey(tree, dockerfilePath, "")
	if err != nil {
		t.Fatalf("DockerfilePluginCacheKey failed: %v", err)
	}
	key2, err := plugins.DockerfilePluginCacheKey(tree, dockerfilePath, "")
	if err != nil {
		t.Fatalf("DockerfilePluginCacheKey failed: %v", err)
	}

	if key1 != key2 {
		t.Errorf("expected stable key on repeated runs, got %s and %s", key1, key2)
	}
}

func TestDockerfilePluginCacheKey_ContextFileChange(t *testing.T) {
	repoRoot := setupTestRepo(t)
	sourcetree.ResetCache()
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	dockerfilePath := "images/plugin/Dockerfile"
	initialKey, err := plugins.DockerfilePluginCacheKey(tree, dockerfilePath, "")
	if err != nil {
		t.Fatal(err)
	}

	// Change a context file
	appFile := filepath.Join(repoRoot, "pkg/app/main.go")
	if err := os.WriteFile(appFile, []byte("package main\n\nfunc Updated() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	sourcetree.ResetCache()
	updatedTree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	newKey, err := plugins.DockerfilePluginCacheKey(updatedTree, dockerfilePath, "")
	if err != nil {
		t.Fatal(err)
	}

	if initialKey == newKey {
		t.Errorf("expected key to change when context file changes, but both were %s", initialKey)
	}
}

func TestDockerfilePluginCacheKey_ContextFileModeChange(t *testing.T) {
	repoRoot := setupTestRepo(t)
	sourcetree.ResetCache()
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	dockerfilePath := "images/plugin/Dockerfile"
	initialKey, err := plugins.DockerfilePluginCacheKey(tree, dockerfilePath, "")
	if err != nil {
		t.Fatal(err)
	}

	// Change mode of a context file
	appFile := filepath.Join(repoRoot, "pkg/app/main.go")
	if err := os.Chmod(appFile, 0755); err != nil {
		t.Fatal(err)
	}

	sourcetree.ResetCache()
	updatedTree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	newKey, err := plugins.DockerfilePluginCacheKey(updatedTree, dockerfilePath, "")
	if err != nil {
		t.Fatal(err)
	}

	if initialKey == newKey {
		t.Errorf("expected key to change when context file mode changes, but both were %s", initialKey)
	}
}

func TestDockerfilePluginCacheKey_DockerfileContentChange(t *testing.T) {
	repoRoot := setupTestRepo(t)
	sourcetree.ResetCache()
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	dockerfilePath := "images/plugin/Dockerfile"
	initialKey, err := plugins.DockerfilePluginCacheKey(tree, dockerfilePath, "")
	if err != nil {
		t.Fatal(err)
	}

	// Change the Dockerfile
	df := filepath.Join(repoRoot, dockerfilePath)
	if err := os.WriteFile(df, []byte("FROM alpine\nRUN echo modified\n"), 0644); err != nil {
		t.Fatal(err)
	}

	sourcetree.ResetCache()
	updatedTree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	newKey, err := plugins.DockerfilePluginCacheKey(updatedTree, dockerfilePath, "")
	if err != nil {
		t.Fatal(err)
	}

	if initialKey == newKey {
		t.Errorf("expected key to change when Dockerfile content changes, but both were %s", initialKey)
	}
}

func TestDockerfilePluginCacheKey_DockerfileChangePath(t *testing.T) {
	repoRoot := setupTestRepo(t)
	sourcetree.ResetCache()
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	key1, err := plugins.DockerfilePluginCacheKey(tree, "images/plugin/Dockerfile", "")
	if err != nil {
		t.Fatal(err)
	}

	// Clean path normalization check
	keyNormalized, err := plugins.DockerfilePluginCacheKey(tree, "images/plugin/./Dockerfile", "")
	if err != nil {
		t.Fatal(err)
	}
	if key1 != keyNormalized {
		t.Errorf("expected clean path to produce same key: %s != %s", key1, keyNormalized)
	}

	// Different path
	key2, err := plugins.DockerfilePluginCacheKey(tree, "images/other/Dockerfile", "")
	if err != nil {
		t.Fatal(err)
	}
	if key1 == key2 {
		t.Errorf("expected different Dockerfile path to produce different key: %s == %s", key1, key2)
	}
}

func TestDockerfilePluginCacheKey_BuildkitHostChange(t *testing.T) {
	repoRoot := setupTestRepo(t)
	sourcetree.ResetCache()
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	dockerfilePath := "images/plugin/Dockerfile"
	keyLocal, err := plugins.DockerfilePluginCacheKey(tree, dockerfilePath, "")
	if err != nil {
		t.Fatal(err)
	}

	keyRemote, err := plugins.DockerfilePluginCacheKey(tree, dockerfilePath, "tcp://127.0.0.1:2375")
	if err != nil {
		t.Fatal(err)
	}

	if keyLocal == keyRemote {
		t.Errorf("expected key to change when BuildKit host changes, but both were %s", keyLocal)
	}
}

func TestDockerfilePluginCacheKey_IgnoredFilesIgnored(t *testing.T) {
	repoRoot := setupTestRepo(t)
	sourcetree.ResetCache()
	tree, err := sourcetree.Open(repoRoot, sourcetree.WithIgnore("ignored.txt"))
	if err != nil {
		t.Fatal(err)
	}

	dockerfilePath := "images/plugin/Dockerfile"
	initialKey, err := plugins.DockerfilePluginCacheKey(tree, dockerfilePath, "")
	if err != nil {
		t.Fatal(err)
	}

	// Modify ignored file
	ignoredFile := filepath.Join(repoRoot, "ignored.txt")
	if err := os.WriteFile(ignoredFile, []byte("changed ignored content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Also modify a default ignored directory (node_modules)
	nodeModulesFile := filepath.Join(repoRoot, "node_modules", "package.json")
	if err := os.MkdirAll(filepath.Dir(nodeModulesFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nodeModulesFile, []byte(`{"name":"foo"}`), 0644); err != nil {
		t.Fatal(err)
	}

	sourcetree.ResetCache()
	updatedTree, err := sourcetree.Open(repoRoot, sourcetree.WithIgnore("ignored.txt"))
	if err != nil {
		t.Fatal(err)
	}

	newKey, err := plugins.DockerfilePluginCacheKey(updatedTree, dockerfilePath, "")
	if err != nil {
		t.Fatal(err)
	}

	if initialKey != newKey {
		t.Errorf("expected ignored file change to not affect key: %s != %s", initialKey, newKey)
	}
}

func TestEnsureDockerfilePluginImage_RecordPresentAndImagePresentMeansNoBuild(t *testing.T) {
	repoRoot := setupTestRepo(t)
	cacheDir := t.TempDir()
	sourcetree.ResetCache()
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	scope := tasks.NewTaskContext(repoRoot, repoRoot, tree)

	var buildCount int64
	fakeBuild := func(ctx context.Context, s *tasks.TaskContext, pluginName, dockerfilePath, buildkitHost string) (string, error) {
		atomic.AddInt64(&buildCount, 1)
		return "sha256:fake-image-id-1234", nil
	}

	existingImages := map[string]bool{
		"sha256:fake-image-id-1234": true,
	}
	fakeLookup := func(ctx context.Context, s *tasks.TaskContext, imageID string) (bool, error) {
		return existingImages[imageID], nil
	}

	// 1. First run: cold cache -> build is called
	imageID1, err := plugins.EnsureDockerfilePluginImage(t.Context(), scope, cacheDir, "myplugin", "images/plugin/Dockerfile", "", fakeBuild, fakeLookup)
	if err != nil {
		t.Fatalf("first EnsureDockerfilePluginImage failed: %v", err)
	}
	if imageID1 != "sha256:fake-image-id-1234" {
		t.Errorf("expected image ID 'sha256:fake-image-id-1234', got: %s", imageID1)
	}
	if atomic.LoadInt64(&buildCount) != 1 {
		t.Fatalf("expected build count 1 on cold run, got: %d", atomic.LoadInt64(&buildCount))
	}

	// Verify record file was written
	key, err := plugins.DockerfilePluginCacheKey(tree, "images/plugin/Dockerfile", "")
	if err != nil {
		t.Fatal(err)
	}
	recordPath := plugins.DockerfilePluginCachePath(cacheDir, key)
	recordData, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("expected record file at %s: %v", recordPath, err)
	}
	if strings.TrimSpace(string(recordData)) != "sha256:fake-image-id-1234" {
		t.Errorf("expected record content 'sha256:fake-image-id-1234', got: %q", string(recordData))
	}

	// 2. Second run: record present and image present in engine -> NO build!
	imageID2, err := plugins.EnsureDockerfilePluginImage(t.Context(), scope, cacheDir, "myplugin", "images/plugin/Dockerfile", "", fakeBuild, fakeLookup)
	if err != nil {
		t.Fatalf("second EnsureDockerfilePluginImage failed: %v", err)
	}
	if imageID2 != "sha256:fake-image-id-1234" {
		t.Errorf("expected image ID 'sha256:fake-image-id-1234', got: %s", imageID2)
	}
	if atomic.LoadInt64(&buildCount) != 1 {
		t.Errorf("expected build count to remain 1 (no rebuild), but was: %d", atomic.LoadInt64(&buildCount))
	}
}

func TestEnsureDockerfilePluginImage_ImageGoneTriggersRebuild(t *testing.T) {
	repoRoot := setupTestRepo(t)
	cacheDir := t.TempDir()
	sourcetree.ResetCache()
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	scope := tasks.NewTaskContext(repoRoot, repoRoot, tree)

	var buildCount int64
	fakeBuild := func(ctx context.Context, s *tasks.TaskContext, pluginName, dockerfilePath, buildkitHost string) (string, error) {
		count := atomic.AddInt64(&buildCount, 1)
		if count == 1 {
			return "sha256:first-image-1111", nil
		}
		return "sha256:second-image-2222", nil
	}

	existingImages := map[string]bool{
		"sha256:first-image-1111": true,
	}
	fakeLookup := func(ctx context.Context, s *tasks.TaskContext, imageID string) (bool, error) {
		return existingImages[imageID], nil
	}

	// First run: builds first image
	id1, err := plugins.EnsureDockerfilePluginImage(t.Context(), scope, cacheDir, "myplugin", "images/plugin/Dockerfile", "", fakeBuild, fakeLookup)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != "sha256:first-image-1111" {
		t.Errorf("expected 'sha256:first-image-1111', got %s", id1)
	}

	// Simulate image deleted from container engine
	existingImages["sha256:first-image-1111"] = false
	existingImages["sha256:second-image-2222"] = true

	// Next run: record present, but image is gone -> rebuilds and rewrites record
	id2, err := plugins.EnsureDockerfilePluginImage(t.Context(), scope, cacheDir, "myplugin", "images/plugin/Dockerfile", "", fakeBuild, fakeLookup)
	if err != nil {
		t.Fatalf("expected delete-and-retry to succeed, got: %v", err)
	}
	if id2 != "sha256:second-image-2222" {
		t.Errorf("expected rebuilt image 'sha256:second-image-2222', got %s", id2)
	}
	if atomic.LoadInt64(&buildCount) != 2 {
		t.Errorf("expected build count 2, got: %d", atomic.LoadInt64(&buildCount))
	}

	// Third run: new image is present -> no build
	id3, err := plugins.EnsureDockerfilePluginImage(t.Context(), scope, cacheDir, "myplugin", "images/plugin/Dockerfile", "", fakeBuild, fakeLookup)
	if err != nil {
		t.Fatal(err)
	}
	if id3 != "sha256:second-image-2222" {
		t.Errorf("expected 'sha256:second-image-2222', got %s", id3)
	}
	if atomic.LoadInt64(&buildCount) != 2 {
		t.Errorf("expected build count to stay 2, got: %d", atomic.LoadInt64(&buildCount))
	}
}

func TestEnsureDockerfilePluginImage_UnreadableRecordTriggersRebuild(t *testing.T) {
	repoRoot := setupTestRepo(t)
	cacheDir := t.TempDir()
	sourcetree.ResetCache()
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	scope := tasks.NewTaskContext(repoRoot, repoRoot, tree)

	key, err := plugins.DockerfilePluginCacheKey(tree, "images/plugin/Dockerfile", "")
	if err != nil {
		t.Fatal(err)
	}
	recordPath := plugins.DockerfilePluginCachePath(cacheDir, key)
	if err := os.MkdirAll(filepath.Dir(recordPath), 0755); err != nil {
		t.Fatal(err)
	}
	// Write empty / whitespace record
	if err := os.WriteFile(recordPath, []byte("   \n"), 0644); err != nil {
		t.Fatal(err)
	}

	var buildCount int64
	fakeBuild := func(ctx context.Context, s *tasks.TaskContext, pluginName, dockerfilePath, buildkitHost string) (string, error) {
		atomic.AddInt64(&buildCount, 1)
		return "sha256:fresh-image-3333", nil
	}
	fakeLookup := func(ctx context.Context, s *tasks.TaskContext, imageID string) (bool, error) {
		return true, nil
	}

	// Should delete corrupt record, build fresh image, write new record, return no error
	id, err := plugins.EnsureDockerfilePluginImage(t.Context(), scope, cacheDir, "myplugin", "images/plugin/Dockerfile", "", fakeBuild, fakeLookup)
	if err != nil {
		t.Fatalf("EnsureDockerfilePluginImage failed on unreadable record: %v", err)
	}
	if id != "sha256:fresh-image-3333" {
		t.Errorf("expected 'sha256:fresh-image-3333', got %s", id)
	}
	if atomic.LoadInt64(&buildCount) != 1 {
		t.Errorf("expected build count 1, got %d", atomic.LoadInt64(&buildCount))
	}
}

func TestEnsureDockerfilePluginImage_BuildFailureReturnsError(t *testing.T) {
	repoRoot := setupTestRepo(t)
	cacheDir := t.TempDir()
	sourcetree.ResetCache()
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	scope := tasks.NewTaskContext(repoRoot, repoRoot, tree)

	expectedErr := errors.New("simulated build failure")
	fakeBuild := func(ctx context.Context, s *tasks.TaskContext, pluginName, dockerfilePath, buildkitHost string) (string, error) {
		return "", expectedErr
	}
	fakeLookup := func(ctx context.Context, s *tasks.TaskContext, imageID string) (bool, error) {
		return true, nil
	}

	_, err = plugins.EnsureDockerfilePluginImage(t.Context(), scope, cacheDir, "myplugin", "images/plugin/Dockerfile", "", fakeBuild, fakeLookup)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected error %v, got %v", expectedErr, err)
	}

	key, err := plugins.DockerfilePluginCacheKey(tree, "images/plugin/Dockerfile", "")
	if err != nil {
		t.Fatal(err)
	}
	recordPath := plugins.DockerfilePluginCachePath(cacheDir, key)
	if _, err := os.Stat(recordPath); !os.IsNotExist(err) {
		t.Errorf("expected no record file to be written on build failure, but file exists")
	}
}

func TestBenchmarkHashingRepoRoot(t *testing.T) {
	// Find repo root containing go.mod
	dir, err := os.Getwd()
	if err != nil {
		t.Skip(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("could not find repo root")
		}
		dir = parent
	}

	sourcetree.ResetCache()
	tree, err := sourcetree.Open(dir)
	if err != nil {
		t.Skipf("cannot open repository root %s: %v", dir, err)
	}

	start := time.Now()
	key, err := plugins.DockerfilePluginCacheKey(tree, "ap/images/ap-astro/Dockerfile", "")
	if err != nil {
		t.Fatalf("DockerfilePluginCacheKey failed on repo root: %v", err)
	}
	duration := time.Since(start)

	t.Logf("Computed cache key %s for %d repo files in %v (cold)", key[:12], len(tree.Files()), duration)

	startWarm := time.Now()
	keyWarm, err := plugins.DockerfilePluginCacheKey(tree, "ap/images/ap-astro/Dockerfile", "")
	if err != nil {
		t.Fatalf("DockerfilePluginCacheKey failed on repo root: %v", err)
	}
	durationWarm := time.Since(startWarm)

	t.Logf("Computed cache key %s for %d repo files in %v (warm)", keyWarm[:12], len(tree.Files()), durationWarm)
}
