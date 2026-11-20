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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/gitsource"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

// BuildPluginImageFunc builds a Dockerfile plugin image and returns its image ID.
type BuildPluginImageFunc func(ctx context.Context, scope *tasks.TaskContext, pluginName, dockerfilePath, buildkitHost string) (string, error)

// ImageLookupFunc checks whether an image ID exists in the local container engine.
type ImageLookupFunc func(ctx context.Context, scope *tasks.TaskContext, imageID string) (bool, error)

// DockerfilePluginBuildRecord records the cache key and built image ID for a Dockerfile plugin.
type DockerfilePluginBuildRecord struct {
	Key     string `json:"key"`
	ImageID string `json:"image_id"`
}

// URLPluginCacheKey computes a deterministic SHA-256 hash representing the inputs to
// a URL Dockerfile plugin build: the resolved commit SHA, the path to the Dockerfile
// inside that repository, and the BuildKit host setting.
func URLPluginCacheKey(commit, dockerfilePath, buildkitHost string) string {
	hasher := sha256.New()
	hasher.Write([]byte("commit\x00" + commit + "\x00"))
	hasher.Write([]byte("path\x00" + filepath.ToSlash(filepath.Clean(dockerfilePath)) + "\x00"))
	hasher.Write([]byte("buildkit_host\x00" + buildkitHost + "\x00"))
	return hex.EncodeToString(hasher.Sum(nil))
}

// DockerfilePluginCacheKey computes a deterministic SHA-256 hash representing the inputs to
// a Dockerfile plugin build: the plugin's Dockerfile path, the content and modes of all files
// in the build context (the repository root view, excluding ignored files), and the BuildKit
// host setting.
func DockerfilePluginCacheKey(ctx context.Context, tree *sourcetree.SourceTree, dockerfilePath, buildkitHost string) (string, error) {
	repoTree := tree.RepoTree()
	repoRoot := repoTree.RepoRoot()
	files := repoTree.Files()

	type fileEntry struct {
		relativePath string
		file         *sourcetree.File
	}
	entries := make([]fileEntry, len(files))
	for index, file := range files {
		relativePath, err := filepath.Rel(repoRoot, file.Path)
		if err != nil {
			return "", fmt.Errorf("calculating relative path for %s from %s: %w", file.Path, repoRoot, err)
		}
		entries[index] = fileEntry{
			relativePath: filepath.ToSlash(relativePath),
			file:         file,
		}
	}
	sort.Slice(entries, func(firstIndex, secondIndex int) bool {
		return entries[firstIndex].relativePath < entries[secondIndex].relativePath
	})

	hasher := sha256.New()
	hasher.Write([]byte("dockerfile\x00" + filepath.ToSlash(filepath.Clean(dockerfilePath)) + "\x00"))
	hasher.Write([]byte("buildkit_host\x00" + buildkitHost + "\x00"))

	var modeBytes [4]byte
	for _, entry := range entries {
		contentHash, err := entry.file.Hash(ctx)
		if err != nil {
			return "", fmt.Errorf("hashing file %s: %w", entry.file.Path, err)
		}
		hasher.Write([]byte(entry.relativePath))
		hasher.Write([]byte{0})
		binary.BigEndian.PutUint32(modeBytes[:], uint32(entry.file.Info().Mode()))
		hasher.Write(modeBytes[:])
		hasher.Write([]byte(contentHash))
		hasher.Write([]byte{0})
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func shortHash(input string) string {
	hash := sha256.Sum256([]byte(input))
	return hex.EncodeToString(hash[:])[:12]
}

// DockerfilePluginCachePath returns the file path where the build record for a Dockerfile plugin is stored under cacheDirectory.
func DockerfilePluginCachePath(cacheDirectory, repoRoot, pluginName, dockerfilePath string) string {
	filename := fmt.Sprintf("%s-%s-%s.json", pluginName, shortHash(repoRoot), shortHash(dockerfilePath))
	return filepath.Join(cacheDirectory, "plugins", "builds", filename)
}

// LookupDockerImage checks whether an image ID is present in the local container engine using docker image inspect.
func LookupDockerImage(ctx context.Context, scope *tasks.TaskContext, imageID string) (bool, error) {
	dockerBin, err := scope.Tool(ctx, "docker")
	if err != nil {
		return false, err
	}
	_, inspectErr := inspectImageID(ctx, dockerBin, imageID)
	if inspectErr != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	return true, nil
}

// EnsureDockerfilePluginImage ensures a Dockerfile plugin image is built and available in the local container engine.
// If a record exists under AP_CACHE_DIR/plugins/builds/ for the plugin matching the current build key and the image
// is present in the engine, the build is skipped and the recorded image ID is returned.
// Otherwise, the image is built and the record is overwritten atomically with fileutils.CreateTempFile.
func EnsureDockerfilePluginImage(
	ctx context.Context,
	scope *tasks.TaskContext,
	pluginName string,
	dockerfilePath string,
	options ContainerPluginOptions,
) (string, error) {
	var key string
	var target *gitsource.Target

	if gitsource.IsURL(dockerfilePath) {
		resolvedTarget, err := gitsource.ResolveTarget(ctx, dockerfilePath)
		if err != nil {
			return "", fmt.Errorf("plugin %q: URL %q: %w", pluginName, dockerfilePath, err)
		}
		target = resolvedTarget
		key = URLPluginCacheKey(target.Commit, target.Path, options.BuildkitHost)
	} else {
		localKey, err := DockerfilePluginCacheKey(ctx, scope.SourceTree(), dockerfilePath, options.BuildkitHost)
		if err != nil {
			return "", fmt.Errorf("computing plugin cache key for %s (%s): %w", pluginName, dockerfilePath, err)
		}
		key = localKey
	}

	recordPath := DockerfilePluginCachePath(options.CacheDirectory, scope.RepoRoot(), pluginName, dockerfilePath)

	recordData, readErr := os.ReadFile(recordPath)
	if readErr != nil {
		if !os.IsNotExist(readErr) {
			scoped.Log(ctx).Fine("failed to read cached plugin record", "path", recordPath, "error", readErr)
		}
	} else {
		var record DockerfilePluginBuildRecord
		if unmarshalErr := json.Unmarshal(recordData, &record); unmarshalErr != nil {
			scoped.Log(ctx).Fine("failed to parse cached plugin record", "path", recordPath, "error", unmarshalErr)
		} else if record.Key == key && record.ImageID != "" {
			exists, lookupErr := options.ImageLookup(ctx, scope, record.ImageID)
			if lookupErr == nil && exists {
				return record.ImageID, nil
			}
			if lookupErr != nil && ctx.Err() != nil {
				return "", ctx.Err()
			}
		}
	}

	buildScope := scope
	buildDockerfile := dockerfilePath

	if target != nil {
		cloneDir, err := gitsource.CloneOrUpdate(ctx, options.CacheDirectory, target)
		if err != nil {
			return "", fmt.Errorf("plugin %q: URL %q: clone failed: %w", pluginName, dockerfilePath, err)
		}

		repoRootFS, err := os.OpenRoot(cloneDir)
		if err != nil {
			return "", fmt.Errorf("plugin %q: URL %q: opening clone directory %s: %w", pluginName, dockerfilePath, cloneDir, err)
		}
		defer func() {
			if closeErr := repoRootFS.Close(); closeErr != nil {
				scoped.Log(ctx).Fine("failed to close clone directory root", "directory", cloneDir, "error", closeErr)
			}
		}()

		fileInfo, err := repoRootFS.Stat(filepath.FromSlash(target.Path))
		if err != nil || !fileInfo.Mode().IsRegular() {
			return "", fmt.Errorf("plugin %q: URL %q: path %q not found at commit %s", pluginName, dockerfilePath, target.Path, target.Commit)
		}

		remoteTree, err := sourcetree.Load(ctx, cloneDir)
		if err != nil {
			return "", fmt.Errorf("plugin %q: URL %q: loading source tree for %s: %w", pluginName, dockerfilePath, cloneDir, err)
		}

		buildScope = tasks.NewTaskContext(cloneDir, cloneDir, remoteTree)
		buildDockerfile = target.Path
	}

	imageID, err := options.BuildImage(ctx, buildScope, pluginName, buildDockerfile, options.BuildkitHost)
	if err != nil {
		return "", err
	}

	newRecord := DockerfilePluginBuildRecord{
		Key:     key,
		ImageID: imageID,
	}
	marshaledRecord, err := json.MarshalIndent(newRecord, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling build record for plugin %q: %w", pluginName, err)
	}

	if err := recordCachedImage(ctx, recordPath, marshaledRecord); err != nil {
		return "", fmt.Errorf("recording cached image for plugin %q: %w", pluginName, err)
	}

	return imageID, nil
}

func recordCachedImage(ctx context.Context, destinationPath string, recordData []byte) error {
	temporaryFile, err := fileutils.CreateTempFile(destinationPath)
	if err != nil {
		return fmt.Errorf("creating temp file for %s: %w", destinationPath, err)
	}
	defer temporaryFile.Cleanup(ctx)

	if _, err := temporaryFile.Write(recordData); err != nil {
		return fmt.Errorf("writing to %s: %w", destinationPath, err)
	}
	if err := temporaryFile.Rename(0644); err != nil {
		return fmt.Errorf("saving %s: %w", destinationPath, err)
	}
	return nil
}
