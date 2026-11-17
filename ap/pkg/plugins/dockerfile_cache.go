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

package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

// BuildPluginImageFunc builds a Dockerfile plugin image and returns its image ID.
type BuildPluginImageFunc func(ctx context.Context, scope *tasks.TaskContext, pluginName, dockerfilePath, buildkitHost string) (string, error)

// ImageLookupFunc checks whether an image ID exists in the local container engine.
type ImageLookupFunc func(ctx context.Context, scope *tasks.TaskContext, imageID string) (bool, error)

// DockerfilePluginCacheKey computes a deterministic SHA-256 hash representing the inputs to
// a Dockerfile plugin build: the plugin's Dockerfile path, the content and modes of all files
// in the build context (the repository root view, excluding ignored files), and the BuildKit
// host setting.
func DockerfilePluginCacheKey(tree *sourcetree.SourceTree, dockerfilePath, buildkitHost string) (string, error) {
	if tree == nil {
		return "", fmt.Errorf("sourcetree is nil")
	}

	repoTree := tree
	if repoTree.RepoTree() != nil {
		repoTree = repoTree.RepoTree()
	}

	repoRoot := repoTree.RepoRoot()
	files := repoTree.Files()

	type fileEntry struct {
		relPath string
		file    *sourcetree.File
	}
	entries := make([]fileEntry, len(files))
	for i, f := range files {
		relPath, err := filepath.Rel(repoRoot, f.Path)
		if err != nil {
			relPath = f.Path
		}
		entries[i] = fileEntry{
			relPath: filepath.ToSlash(relPath),
			file:    f,
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].relPath < entries[j].relPath
	})

	hasher := sha256.New()
	hasher.Write([]byte("dockerfile\x00" + filepath.ToSlash(filepath.Clean(dockerfilePath)) + "\x00"))
	hasher.Write([]byte("buildkit_host\x00" + buildkitHost + "\x00"))

	var modeBytes [4]byte
	for _, entry := range entries {
		contentHash, err := entry.file.Hash()
		if err != nil {
			return "", fmt.Errorf("hashing file %s: %w", entry.file.Path, err)
		}
		hasher.Write([]byte(entry.relPath))
		hasher.Write([]byte{0})
		binary.BigEndian.PutUint32(modeBytes[:], uint32(entry.file.Info().Mode()))
		hasher.Write(modeBytes[:])
		hasher.Write([]byte(contentHash))
		hasher.Write([]byte{0})
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// DockerfilePluginCachePath returns the file path where the built image ID for key is recorded under cacheDirectory.
func DockerfilePluginCachePath(cacheDirectory, key string) string {
	sanitizedKey := strings.ReplaceAll(strings.ReplaceAll(key, ":", "-"), "/", "-")
	return filepath.Join(cacheDirectory, "plugins", "builds", sanitizedKey)
}

func defaultDockerImageLookup(ctx context.Context, scope *tasks.TaskContext, imageID string) (bool, error) {
	dockerBin, err := scope.Tool(ctx, "docker")
	if err != nil {
		return false, err
	}
	_, err = inspectImageID(ctx, dockerBin, imageID)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	return true, nil
}

// EnsureDockerfilePluginImage ensures a Dockerfile plugin image is built and available in the local container engine.
// If a valid record exists under AP_CACHE_DIR/plugins/ for the current build inputs key and the image still exists
// in the engine, the build is skipped and the recorded image ID is returned.
// Otherwise, the image is built and the record is written atomically with fileutils.CreateTempFile.
func EnsureDockerfilePluginImage(
	ctx context.Context,
	scope *tasks.TaskContext,
	cacheDirectory string,
	pluginName string,
	dockerfilePath string,
	buildkitHost string,
	build BuildPluginImageFunc,
	lookup ImageLookupFunc,
) (string, error) {
	if scope == nil || scope.SourceTree() == nil {
		return "", fmt.Errorf("task context or source tree is nil")
	}

	if build == nil {
		build = buildPluginImage
	}
	if lookup == nil {
		lookup = defaultDockerImageLookup
	}
	if cacheDirectory == "" {
		var err error
		cacheDirectory, err = fileutils.CacheDir()
		if err != nil {
			return "", fmt.Errorf("resolving cache directory: %w", err)
		}
	}

	key, err := DockerfilePluginCacheKey(scope.SourceTree(), dockerfilePath, buildkitHost)
	if err != nil {
		return "", fmt.Errorf("computing plugin cache key for %s (%s): %w", pluginName, dockerfilePath, err)
	}

	recordPath := DockerfilePluginCachePath(cacheDirectory, key)

	// Check if cached record exists and image is still in the engine
	data, readErr := os.ReadFile(recordPath)
	if readErr != nil {
		if !os.IsNotExist(readErr) {
			scoped.Log(ctx).V(2).Info("failed to read cached plugin image ID", "path", recordPath, "error", readErr)
		}
	} else {
		recordedID := strings.TrimSpace(string(data))
		if recordedID != "" {
			exists, lookupErr := lookup(ctx, scope, recordedID)
			if lookupErr == nil && exists {
				return recordedID, nil
			}
			if lookupErr != nil && ctx.Err() != nil {
				return "", ctx.Err()
			}
		}
		// Record cannot be read properly or image is gone: delete-and-retry
		if removeErr := os.Remove(recordPath); removeErr != nil && !os.IsNotExist(removeErr) {
			scoped.Log(ctx).V(2).Info("failed to remove stale plugin cache record", "path", recordPath, "error", removeErr)
		}
	}

	// Build the image
	imageID, err := build(ctx, scope, pluginName, dockerfilePath, buildkitHost)
	if err != nil {
		return "", err
	}

	// Record the image ID atomically
	if err := recordCachedImageID(recordPath, imageID); err != nil {
		return "", fmt.Errorf("recording cached image ID for plugin %q: %w", pluginName, err)
	}

	return imageID, nil
}

func recordCachedImageID(dest, imageID string) error {
	tempFile, err := fileutils.CreateTempFile(dest)
	if err != nil {
		return fmt.Errorf("creating temp file for %s: %w", dest, err)
	}
	defer tempFile.Cleanup()

	if _, err := tempFile.Write([]byte(imageID + "\n")); err != nil {
		return fmt.Errorf("writing to %s: %w", dest, err)
	}
	if err := tempFile.Rename(0644); err != nil {
		return fmt.Errorf("saving %s: %w", dest, err)
	}
	return nil
}
