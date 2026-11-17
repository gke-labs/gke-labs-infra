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
	"encoding/json"
	"os"
	"path/filepath"
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
		"images/plugin/Dockerfile": "FROM alpine\nRUN echo hello\n",
		"pkg/app/main.go":          "package main\n\nfunc main() {}\n",
		"docs/readme.md":           "# Demo\n",
	}

	for relativePath, content := range files {
		absPath := filepath.Join(repoRoot, relativePath)
		if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	return repoRoot
}

func TestDockerfilePluginCacheKey(t *testing.T) {
	dockerfilePath := "images/plugin/Dockerfile"

	t.Run("stable across repeated invocations", func(t *testing.T) {
		repoRoot := setupTestRepo(t)
		sourcetree.ResetCache()
		tree, err := sourcetree.Open(repoRoot)
		if err != nil {
			t.Fatal(err)
		}

		firstKey, err := plugins.DockerfilePluginCacheKey(tree, dockerfilePath, "")
		if err != nil {
			t.Fatalf("first DockerfilePluginCacheKey failed: %v", err)
		}
		secondKey, err := plugins.DockerfilePluginCacheKey(tree, dockerfilePath, "")
		if err != nil {
			t.Fatalf("second DockerfilePluginCacheKey failed: %v", err)
		}
		if firstKey != secondKey {
			t.Errorf("expected stable key, got %s and %s", firstKey, secondKey)
		}
	})

	t.Run("changes when context file content changes", func(t *testing.T) {
		repoRoot := setupTestRepo(t)
		sourcetree.ResetCache()
		tree, err := sourcetree.Open(repoRoot)
		if err != nil {
			t.Fatal(err)
		}

		initialKey, err := plugins.DockerfilePluginCacheKey(tree, dockerfilePath, "")
		if err != nil {
			t.Fatal(err)
		}

		appFile := filepath.Join(repoRoot, "pkg/app/main.go")
		if err := os.WriteFile(appFile, []byte("package main\n\nfunc Modified() {}\n"), 0644); err != nil {
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
			t.Errorf("expected key to change when context file content changes")
		}
	})

	t.Run("changes when context file mode changes", func(t *testing.T) {
		repoRoot := setupTestRepo(t)
		sourcetree.ResetCache()
		tree, err := sourcetree.Open(repoRoot)
		if err != nil {
			t.Fatal(err)
		}

		initialKey, err := plugins.DockerfilePluginCacheKey(tree, dockerfilePath, "")
		if err != nil {
			t.Fatal(err)
		}

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
			t.Errorf("expected key to change when context file mode changes")
		}
	})

	t.Run("changes when Dockerfile content changes", func(t *testing.T) {
		repoRoot := setupTestRepo(t)
		sourcetree.ResetCache()
		tree, err := sourcetree.Open(repoRoot)
		if err != nil {
			t.Fatal(err)
		}

		initialKey, err := plugins.DockerfilePluginCacheKey(tree, dockerfilePath, "")
		if err != nil {
			t.Fatal(err)
		}

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
			t.Errorf("expected key to change when Dockerfile content changes")
		}
	})

	t.Run("changes when Dockerfile path changes", func(t *testing.T) {
		repoRoot := setupTestRepo(t)
		sourcetree.ResetCache()
		tree, err := sourcetree.Open(repoRoot)
		if err != nil {
			t.Fatal(err)
		}

		keyA, err := plugins.DockerfilePluginCacheKey(tree, "images/plugin/Dockerfile", "")
		if err != nil {
			t.Fatal(err)
		}
		keyB, err := plugins.DockerfilePluginCacheKey(tree, "images/other/Dockerfile", "")
		if err != nil {
			t.Fatal(err)
		}
		if keyA == keyB {
			t.Errorf("expected different keys for different Dockerfile paths")
		}
	})

	t.Run("changes when BuildKit host changes", func(t *testing.T) {
		repoRoot := setupTestRepo(t)
		sourcetree.ResetCache()
		tree, err := sourcetree.Open(repoRoot)
		if err != nil {
			t.Fatal(err)
		}

		localKey, err := plugins.DockerfilePluginCacheKey(tree, dockerfilePath, "")
		if err != nil {
			t.Fatal(err)
		}
		remoteKey, err := plugins.DockerfilePluginCacheKey(tree, dockerfilePath, "tcp://127.0.0.1:2375")
		if err != nil {
			t.Fatal(err)
		}
		if localKey == remoteKey {
			t.Errorf("expected key to change when BuildKit host changes")
		}
	})

	t.Run("ignores ignored files", func(t *testing.T) {
		repoRoot := setupTestRepo(t)
		sourcetree.ResetCache()
		tree, err := sourcetree.Open(repoRoot, sourcetree.WithIgnore("ignored.txt"))
		if err != nil {
			t.Fatal(err)
		}

		initialKey, err := plugins.DockerfilePluginCacheKey(tree, dockerfilePath, "")
		if err != nil {
			t.Fatal(err)
		}

		ignoredPath := filepath.Join(repoRoot, "ignored.txt")
		if err := os.WriteFile(ignoredPath, []byte("ignored content\n"), 0644); err != nil {
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
			t.Errorf("expected ignored file change not to affect key: %s != %s", initialKey, newKey)
		}
	})
}

func TestEnsureDockerfilePluginImage(t *testing.T) {
	dockerfilePath := "images/plugin/Dockerfile"
	pluginName := "myplugin"

	t.Run("record present and image present means no build", func(t *testing.T) {
		repoRoot := setupTestRepo(t)
		cacheDirectory := t.TempDir()
		sourcetree.ResetCache()
		tree, err := sourcetree.Open(repoRoot)
		if err != nil {
			t.Fatal(err)
		}
		scope := tasks.NewTaskContext(repoRoot, repoRoot, tree)

		var buildCount int64
		fakeBuild := func(_ context.Context, _ *tasks.TaskContext, _, _, _ string) (string, error) {
			atomic.AddInt64(&buildCount, 1)
			return "sha256:fake-image-id-1234", nil
		}
		fakeLookup := func(_ context.Context, _ *tasks.TaskContext, imageID string) (bool, error) {
			return imageID == "sha256:fake-image-id-1234", nil
		}

		options := plugins.ContainerPluginOptions{
			CacheDirectory: cacheDirectory,
			BuildImage:     fakeBuild,
			ImageLookup:    fakeLookup,
		}

		firstImageID, err := plugins.EnsureDockerfilePluginImage(t.Context(), scope, pluginName, dockerfilePath, options)
		if err != nil {
			t.Fatalf("first EnsureDockerfilePluginImage failed: %v", err)
		}
		if firstImageID != "sha256:fake-image-id-1234" {
			t.Errorf("expected image ID 'sha256:fake-image-id-1234', got %s", firstImageID)
		}
		if atomic.LoadInt64(&buildCount) != 1 {
			t.Fatalf("expected build count 1 on cold run, got %d", atomic.LoadInt64(&buildCount))
		}

		recordPath := plugins.DockerfilePluginCachePath(cacheDirectory, repoRoot, pluginName, dockerfilePath)
		recordData, err := os.ReadFile(recordPath)
		if err != nil {
			t.Fatalf("expected record file at %s: %v", recordPath, err)
		}
		var record plugins.DockerfilePluginBuildRecord
		if err := json.Unmarshal(recordData, &record); err != nil {
			t.Fatalf("parsing record file: %v", err)
		}
		if record.ImageID != "sha256:fake-image-id-1234" {
			t.Errorf("expected recorded image ID 'sha256:fake-image-id-1234', got %s", record.ImageID)
		}

		// Second invocation: record and image present -> build is skipped
		secondImageID, err := plugins.EnsureDockerfilePluginImage(t.Context(), scope, pluginName, dockerfilePath, options)
		if err != nil {
			t.Fatalf("second EnsureDockerfilePluginImage failed: %v", err)
		}
		if secondImageID != "sha256:fake-image-id-1234" {
			t.Errorf("expected image ID 'sha256:fake-image-id-1234', got %s", secondImageID)
		}
		if atomic.LoadInt64(&buildCount) != 1 {
			t.Errorf("expected build count to stay 1, got %d", atomic.LoadInt64(&buildCount))
		}
	})

	t.Run("missing image triggers rebuild", func(t *testing.T) {
		repoRoot := setupTestRepo(t)
		cacheDirectory := t.TempDir()
		sourcetree.ResetCache()
		tree, err := sourcetree.Open(repoRoot)
		if err != nil {
			t.Fatal(err)
		}
		scope := tasks.NewTaskContext(repoRoot, repoRoot, tree)

		var buildCount int64
		fakeBuild := func(_ context.Context, _ *tasks.TaskContext, _, _, _ string) (string, error) {
			count := atomic.AddInt64(&buildCount, 1)
			if count == 1 {
				return "sha256:first-image-1111", nil
			}
			return "sha256:second-image-2222", nil
		}

		engineImages := map[string]bool{
			"sha256:first-image-1111": true,
		}
		fakeLookup := func(_ context.Context, _ *tasks.TaskContext, imageID string) (bool, error) {
			return engineImages[imageID], nil
		}

		options := plugins.ContainerPluginOptions{
			CacheDirectory: cacheDirectory,
			BuildImage:     fakeBuild,
			ImageLookup:    fakeLookup,
		}

		firstID, err := plugins.EnsureDockerfilePluginImage(t.Context(), scope, pluginName, dockerfilePath, options)
		if err != nil {
			t.Fatal(err)
		}
		if firstID != "sha256:first-image-1111" {
			t.Errorf("expected 'sha256:first-image-1111', got %s", firstID)
		}

		// Simulate image deleted / pruned from local engine
		engineImages["sha256:first-image-1111"] = false
		engineImages["sha256:second-image-2222"] = true

		secondID, err := plugins.EnsureDockerfilePluginImage(t.Context(), scope, pluginName, dockerfilePath, options)
		if err != nil {
			t.Fatalf("expected rebuild when image is gone, got: %v", err)
		}
		if secondID != "sha256:second-image-2222" {
			t.Errorf("expected 'sha256:second-image-2222', got %s", secondID)
		}
		if atomic.LoadInt64(&buildCount) != 2 {
			t.Errorf("expected build count 2 after missing image rebuild, got %d", atomic.LoadInt64(&buildCount))
		}
	})

	t.Run("unreadable record triggers rebuild", func(t *testing.T) {
		repoRoot := setupTestRepo(t)
		cacheDirectory := t.TempDir()
		sourcetree.ResetCache()
		tree, err := sourcetree.Open(repoRoot)
		if err != nil {
			t.Fatal(err)
		}
		scope := tasks.NewTaskContext(repoRoot, repoRoot, tree)

		recordPath := plugins.DockerfilePluginCachePath(cacheDirectory, repoRoot, pluginName, dockerfilePath)
		if err := os.MkdirAll(filepath.Dir(recordPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(recordPath, []byte("not valid json"), 0644); err != nil {
			t.Fatal(err)
		}

		var buildCount int64
		fakeBuild := func(_ context.Context, _ *tasks.TaskContext, _, _, _ string) (string, error) {
			atomic.AddInt64(&buildCount, 1)
			return "sha256:fresh-image-3333", nil
		}
		fakeLookup := func(_ context.Context, _ *tasks.TaskContext, _ string) (bool, error) {
			return true, nil
		}

		options := plugins.ContainerPluginOptions{
			CacheDirectory: cacheDirectory,
			BuildImage:     fakeBuild,
			ImageLookup:    fakeLookup,
		}

		imageID, err := plugins.EnsureDockerfilePluginImage(t.Context(), scope, pluginName, dockerfilePath, options)
		if err != nil {
			t.Fatalf("expected rebuild on unreadable record, got: %v", err)
		}
		if imageID != "sha256:fresh-image-3333" {
			t.Errorf("expected 'sha256:fresh-image-3333', got %s", imageID)
		}
		if atomic.LoadInt64(&buildCount) != 1 {
			t.Errorf("expected build count 1, got %d", atomic.LoadInt64(&buildCount))
		}
	})

	t.Run("key mismatch triggers rebuild and overwrites record", func(t *testing.T) {
		repoRoot := setupTestRepo(t)
		cacheDirectory := t.TempDir()
		sourcetree.ResetCache()
		tree, err := sourcetree.Open(repoRoot)
		if err != nil {
			t.Fatal(err)
		}
		scope := tasks.NewTaskContext(repoRoot, repoRoot, tree)

		var buildCount int64
		fakeBuild := func(_ context.Context, _ *tasks.TaskContext, _, _, _ string) (string, error) {
			count := atomic.AddInt64(&buildCount, 1)
			if count == 1 {
				return "sha256:initial-image", nil
			}
			return "sha256:updated-image", nil
		}
		fakeLookup := func(_ context.Context, _ *tasks.TaskContext, _ string) (bool, error) {
			return true, nil
		}

		options := plugins.ContainerPluginOptions{
			CacheDirectory: cacheDirectory,
			BuildImage:     fakeBuild,
			ImageLookup:    fakeLookup,
		}

		firstID, err := plugins.EnsureDockerfilePluginImage(t.Context(), scope, pluginName, dockerfilePath, options)
		if err != nil {
			t.Fatal(err)
		}
		if firstID != "sha256:initial-image" {
			t.Errorf("expected 'sha256:initial-image', got %s", firstID)
		}

		// Modify a context file
		appFile := filepath.Join(repoRoot, "pkg/app/main.go")
		if err := os.WriteFile(appFile, []byte("package main\n\nfunc Changed() {}\n"), 0644); err != nil {
			t.Fatal(err)
		}
		sourcetree.ResetCache()
		updatedTree, err := sourcetree.Open(repoRoot)
		if err != nil {
			t.Fatal(err)
		}
		updatedScope := tasks.NewTaskContext(repoRoot, repoRoot, updatedTree)

		secondID, err := plugins.EnsureDockerfilePluginImage(t.Context(), updatedScope, pluginName, dockerfilePath, options)
		if err != nil {
			t.Fatal(err)
		}
		if secondID != "sha256:updated-image" {
			t.Errorf("expected 'sha256:updated-image', got %s", secondID)
		}
		if atomic.LoadInt64(&buildCount) != 2 {
			t.Errorf("expected build count 2 after context file change, got %d", atomic.LoadInt64(&buildCount))
		}
	})

	t.Run("distinct checkouts keep separate records", func(t *testing.T) {
		repoRootA := setupTestRepo(t)
		repoRootB := setupTestRepo(t)
		cacheDirectory := t.TempDir()

		sourcetree.ResetCache()
		treeA, err := sourcetree.Open(repoRootA)
		if err != nil {
			t.Fatal(err)
		}
		scopeA := tasks.NewTaskContext(repoRootA, repoRootA, treeA)

		sourcetree.ResetCache()
		treeB, err := sourcetree.Open(repoRootB)
		if err != nil {
			t.Fatal(err)
		}
		scopeB := tasks.NewTaskContext(repoRootB, repoRootB, treeB)

		var buildCountA, buildCountB int64
		fakeBuild := func(_ context.Context, scope *tasks.TaskContext, _, _, _ string) (string, error) {
			if scope.RepoRoot() == repoRootA {
				atomic.AddInt64(&buildCountA, 1)
				return "sha256:image-a", nil
			}
			atomic.AddInt64(&buildCountB, 1)
			return "sha256:image-b", nil
		}
		fakeLookup := func(_ context.Context, _ *tasks.TaskContext, _ string) (bool, error) {
			return true, nil
		}

		options := plugins.ContainerPluginOptions{
			CacheDirectory: cacheDirectory,
			BuildImage:     fakeBuild,
			ImageLookup:    fakeLookup,
		}

		// Build in checkout A
		imageIDA, err := plugins.EnsureDockerfilePluginImage(t.Context(), scopeA, pluginName, dockerfilePath, options)
		if err != nil {
			t.Fatal(err)
		}
		if imageIDA != "sha256:image-a" {
			t.Errorf("expected 'sha256:image-a', got %s", imageIDA)
		}
		if atomic.LoadInt64(&buildCountA) != 1 {
			t.Errorf("expected buildCountA = 1, got %d", atomic.LoadInt64(&buildCountA))
		}

		// Build in checkout B
		imageIDB, err := plugins.EnsureDockerfilePluginImage(t.Context(), scopeB, pluginName, dockerfilePath, options)
		if err != nil {
			t.Fatal(err)
		}
		if imageIDB != "sha256:image-b" {
			t.Errorf("expected 'sha256:image-b', got %s", imageIDB)
		}
		if atomic.LoadInt64(&buildCountB) != 1 {
			t.Errorf("expected buildCountB = 1, got %d", atomic.LoadInt64(&buildCountB))
		}

		// Verify distinct record files were created
		recordPathA := plugins.DockerfilePluginCachePath(cacheDirectory, repoRootA, pluginName, dockerfilePath)
		recordPathB := plugins.DockerfilePluginCachePath(cacheDirectory, repoRootB, pluginName, dockerfilePath)
		if recordPathA == recordPathB {
			t.Fatalf("expected different record paths for different checkouts, both were %s", recordPathA)
		}

		recordDataA, err := os.ReadFile(recordPathA)
		if err != nil {
			t.Fatalf("reading record A: %v", err)
		}
		var recordA plugins.DockerfilePluginBuildRecord
		if err := json.Unmarshal(recordDataA, &recordA); err != nil {
			t.Fatalf("parsing record A: %v", err)
		}
		if recordA.ImageID != "sha256:image-a" {
			t.Errorf("expected record A image ID 'sha256:image-a', got %s", recordA.ImageID)
		}

		// Re-run in checkout A: should use cached record A and NOT rebuild
		cachedIDA, err := plugins.EnsureDockerfilePluginImage(t.Context(), scopeA, pluginName, dockerfilePath, options)
		if err != nil {
			t.Fatal(err)
		}
		if cachedIDA != "sha256:image-a" {
			t.Errorf("expected 'sha256:image-a', got %s", cachedIDA)
		}
		if atomic.LoadInt64(&buildCountA) != 1 {
			t.Errorf("expected buildCountA to remain 1 (no rebuild after checkout B ran), got %d", atomic.LoadInt64(&buildCountA))
		}

		// Re-run in checkout B: should use cached record B and NOT rebuild
		cachedIDB, err := plugins.EnsureDockerfilePluginImage(t.Context(), scopeB, pluginName, dockerfilePath, options)
		if err != nil {
			t.Fatal(err)
		}
		if cachedIDB != "sha256:image-b" {
			t.Errorf("expected 'sha256:image-b', got %s", cachedIDB)
		}
		if atomic.LoadInt64(&buildCountB) != 1 {
			t.Errorf("expected buildCountB to remain 1, got %d", atomic.LoadInt64(&buildCountB))
		}
	})
}

func TestBenchmarkHashingRepoRoot(t *testing.T) {
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
}
