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
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

const (
	layerCacheDirectoryName = "ap-layer-cache"
	// estimatedRestoreShareSeconds is the estimated per-image share of the cache restore time (download and decompression),
	// measured across benchmark runs on GitHub Actions runners.
	estimatedRestoreShareSeconds = 5.0
)

type imageCacheMetadata struct {
	DockerfileHash string             `json:"dockerfileHash"`
	ExportDuration float64            `json:"exportDurationSeconds,omitempty"`
	StepDurations  map[string]float64 `json:"stepDurations,omitempty"`
	Dropped        bool               `json:"dropped,omitempty"`
}

type layerCache struct {
	importArg        string
	exportArg        string
	finalDirectory   string
	stagingDirectory string
	dockerfileHash   string
	existingMetadata imageCacheMetadata
	hasMetadata      bool
}

func (c layerCache) active() bool {
	return c.importArg != "" || c.exportArg != ""
}

func (c layerCache) applyDockerArgs(args []string) []string {
	if !c.active() {
		return args
	}
	args = append(args, "--progress=plain")
	if c.importArg != "" {
		args = append(args, "--cache-from", c.importArg)
	}
	if c.exportArg != "" {
		args = append(args, "--cache-to", c.exportArg)
	}
	return args
}

func (c layerCache) applyBuildctlArgs(args []string) []string {
	if !c.active() {
		return args
	}
	if c.importArg != "" {
		args = append(args, "--import-cache", c.importArg)
	}
	if c.exportArg != "" {
		args = append(args, "--export-cache", c.exportArg)
	}
	return args
}

func (c layerCache) wireStderr(scope *tasks.TaskContext, progressBuffer *bytes.Buffer) io.Writer {
	if c.active() {
		return io.MultiWriter(scope.Stderr(), progressBuffer)
	}
	return scope.Stderr()
}

func (c layerCache) cleanup(ctx context.Context) {
	if c.stagingDirectory != "" {
		if err := os.RemoveAll(c.stagingDirectory); err != nil && !errors.Is(err, os.ErrNotExist) {
			scoped.Log(ctx).Finest("failed to clean staging directory", "dir", c.stagingDirectory, "error", err)
		}
	}
}

func (c layerCache) drop(ctx context.Context) error {
	c.cleanup(ctx)
	if err := os.RemoveAll(c.finalDirectory); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(c.finalDirectory, 0755); err != nil {
		return err
	}
	return writeImageCacheMetadata(filepath.Join(c.finalDirectory, "metadata.json"), imageCacheMetadata{
		DockerfileHash: c.dockerfileHash,
		Dropped:        true,
	})
}

func (c layerCache) promote(metadata imageCacheMetadata) error {
	if c.stagingDirectory == "" {
		return nil
	}
	if ok, err := fileutils.FileExists(c.stagingDirectory); err != nil {
		return err
	} else if ok {
		if err := writeImageCacheMetadata(filepath.Join(c.stagingDirectory, "metadata.json"), metadata); err != nil {
			return err
		}
		if err := os.RemoveAll(c.finalDirectory); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return os.Rename(c.stagingDirectory, c.finalDirectory)
	}
	return nil
}

func getLayerCacheRoot() string {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return ""
	}
	runnerTemp := os.Getenv("RUNNER_TEMP")
	if runnerTemp == "" {
		return ""
	}
	return filepath.Join(runnerTemp, layerCacheDirectoryName)
}

func hashDockerfile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	hasher := sha256.Sum256(data)
	return hex.EncodeToString(hasher[:]), nil
}

func readImageCacheMetadata(path string) (imageCacheMetadata, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return imageCacheMetadata{}, false
	}
	var metadata imageCacheMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return imageCacheMetadata{}, false
	}
	return metadata, true
}

func writeImageCacheMetadata(path string, metadata imageCacheMetadata) error {
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// setupLayerCache configures BuildKit import and export cache parameters for an image build,
// bypassing caching if disabled or if the image was previously dropped for the current Dockerfile.
func setupLayerCache(ctx context.Context, imageName, dockerfilePath string) layerCache {
	cacheRoot := getLayerCacheRoot()
	if cacheRoot == "" {
		return layerCache{}
	}
	dockerfileHash, err := hashDockerfile(dockerfilePath)
	if err != nil {
		scoped.Log(ctx).Warn("failed to hash Dockerfile for layer cache", "path", dockerfilePath, "error", err)
		return layerCache{}
	}
	finalDirectory := filepath.Join(cacheRoot, imageName)
	stagingDirectory := filepath.Join(cacheRoot, imageName+"-stage")
	existingMetadata, hasMetadata := readImageCacheMetadata(filepath.Join(finalDirectory, "metadata.json"))
	if hasMetadata && existingMetadata.Dropped && existingMetadata.DockerfileHash == dockerfileHash {
		return layerCache{}
	}
	var importArg string
	if ok, err := fileutils.FileExists(filepath.Join(finalDirectory, "index.json")); err == nil && ok {
		importArg = "type=local,src=" + finalDirectory
	}
	var exportArg string
	if isCacheSaveAllowed() {
		exportArg = fmt.Sprintf("type=local,dest=%s,mode=max,compression=uncompressed", stagingDirectory)
		if err := os.RemoveAll(stagingDirectory); err != nil && !errors.Is(err, os.ErrNotExist) {
			scoped.Log(ctx).Warn("failed to clean staging directory", "dir", stagingDirectory, "error", err)
		}
		if err := os.MkdirAll(stagingDirectory, 0755); err != nil {
			scoped.Log(ctx).Warn("failed to create staging layer cache directory", "dir", stagingDirectory, "error", err)
			return layerCache{}
		}
	}
	if importArg == "" && exportArg == "" {
		return layerCache{}
	}
	return layerCache{
		importArg:        importArg,
		exportArg:        exportArg,
		finalDirectory:   finalDirectory,
		stagingDirectory: stagingDirectory,
		dockerfileHash:   dockerfileHash,
		existingMetadata: existingMetadata,
		hasMetadata:      hasMetadata,
	}
}

// evaluate implements the saved-versus-cost keep rule for BuildKit image layer caches.
//
// The rule compares the time saved by cache reuse against the cache's amortized cost:
//   - saved is the sum of previous execution durations for all steps reported as CACHED in this build.
//   - cost is the previous export duration plus estimatedRestoreShareSeconds (5.0s, the measured
//     per-image download and decompression share across GitHub Actions runner benchmark runs).
//
// The function executes one of three outcomes:
//  1. First export (no existing metadata): records measured step durations and export duration,
//     promotes the staging directory to final, and marks the cache as kept.
//  2. Unprofitable (saved <= cost): drops the cache by removing index.json and writing metadata.json
//     with Dropped=true and the Dockerfile hash. On subsequent builds with an unchanged Dockerfile,
//     setupLayerCache detects this dropped state and bypasses cache import/export flags entirely.
//  3. Profitable (saved > cost): keeps the cache. If new steps were built, updates step durations,
//     refreshes the export duration, and promotes staging; if all steps were cached, discards staging
//     to avoid re-export overhead while preserving the existing cache.
func (c layerCache) evaluate(ctx context.Context, imageName string, progress io.Reader) {
	if !c.active() || c.exportArg == "" {
		return
	}
	buildKitProgress := parseBuildKitProgress(progress)
	exportDuration := buildKitProgress.exportDuration

	var savedSeconds float64
	var costSeconds float64
	var action string

	if !c.hasMetadata || len(c.existingMetadata.StepDurations) == 0 {
		costSeconds = exportDuration
		action = "kept"
		newMetadata := imageCacheMetadata{
			DockerfileHash: c.dockerfileHash,
			ExportDuration: exportDuration,
			StepDurations:  buildKitProgress.stepDurations,
		}
		if err := c.promote(newMetadata); err != nil {
			scoped.Log(ctx).Warn("failed to promote layer cache", "image", imageName, "error", err)
		}
	} else {
		for step := range buildKitProgress.cachedSteps {
			if duration, ok := c.existingMetadata.StepDurations[step]; ok {
				savedSeconds += duration
			}
		}
		costSeconds = c.existingMetadata.ExportDuration + estimatedRestoreShareSeconds

		if savedSeconds <= costSeconds {
			action = "dropped"
			if err := c.drop(ctx); err != nil {
				scoped.Log(ctx).Warn("failed to drop layer cache", "image", imageName, "error", err)
			}
		} else {
			action = "kept"
			hasNewSteps := false
			for step := range buildKitProgress.stepDurations {
				if !buildKitProgress.cachedSteps[step] {
					hasNewSteps = true
					break
				}
			}

			if hasNewSteps {
				newMetadata := c.existingMetadata
				if newMetadata.StepDurations == nil {
					newMetadata.StepDurations = make(map[string]float64)
				}
				for step, duration := range buildKitProgress.stepDurations {
					newMetadata.StepDurations[step] = duration
				}
				newMetadata.ExportDuration = exportDuration
				newMetadata.DockerfileHash = c.dockerfileHash
				if err := c.promote(newMetadata); err != nil {
					scoped.Log(ctx).Warn("failed to promote layer cache", "image", imageName, "error", err)
				}
			} else {
				c.cleanup(ctx)
			}
		}
	}

	scoped.Log(ctx).Info("evaluated layer cache",
		"image", imageName,
		"saved", formatDuration(time.Duration(savedSeconds*float64(time.Second))),
		"cost", formatDuration(time.Duration(costSeconds*float64(time.Second))),
		"action", action,
	)
}

// FinalizeLayerCache inspects all image directories in the layer cache root, computes a deterministic
// cache key based on the exported cache manifests (index.json) and dropped states, and writes AP_LAYER_CACHE_KEY
// to GITHUB_ENV once after all parallel image builds have finished.
func FinalizeLayerCache(ctx context.Context) error {
	if !isCacheSaveAllowed() {
		return nil
	}
	cacheRoot := getLayerCacheRoot()
	runnerOS := os.Getenv("RUNNER_OS")
	if cacheRoot == "" || runnerOS == "" {
		return nil
	}
	entries, err := os.ReadDir(cacheRoot)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	hasAnyContent := false
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasSuffix(entry.Name(), "-stage") {
			continue
		}
		imageDirectory := filepath.Join(cacheRoot, entry.Name())
		indexPath := filepath.Join(imageDirectory, "index.json")
		indexData, err := os.ReadFile(indexPath)
		if err == nil {
			hasAnyContent = true
			hasher.Write([]byte(entry.Name()))
			hasher.Write(indexData)
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if metadata, ok := readImageCacheMetadata(filepath.Join(imageDirectory, "metadata.json")); ok && metadata.Dropped {
			hasAnyContent = true
			hasher.Write([]byte(entry.Name()))
			hasher.Write([]byte("dropped:" + metadata.DockerfileHash))
		}
	}
	if !hasAnyContent {
		return nil
	}
	hash := hex.EncodeToString(hasher.Sum(nil))[:16]
	return setGitHubActionsEnv("AP_LAYER_CACHE_KEY", fmt.Sprintf("ap-layer-cache-%s-%s", runnerOS, hash))
}

func setGitHubActionsEnv(name, value string) error {
	githubEnv := os.Getenv("GITHUB_ENV")
	if githubEnv == "" {
		return nil
	}
	f, err := os.OpenFile(githubEnv, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	_, printErr := fmt.Fprintf(f, "%s=%s\n", name, value)
	closeErr := f.Close()
	if printErr != nil {
		return printErr
	}
	return closeErr
}

func isCacheSaveAllowed() bool {
	return os.Getenv("GITHUB_ACTIONS") == "true" && os.Getenv("GITHUB_EVENT_NAME") == "push"
}

type buildKitProgress struct {
	stepDurations  map[string]float64
	cachedSteps    map[string]bool
	exportDuration float64
}

// parseBuildKitProgress parses a BuildKit progress stream (plain text), extracting per-step execution
// durations, cached step markers, and cache export durations.
func parseBuildKitProgress(reader io.Reader) buildKitProgress {
	scanner := bufio.NewScanner(reader)
	stepDescriptions := make(map[string]string)
	stepDurations := make(map[string]float64)
	cachedSteps := make(map[string]bool)
	var exportDuration float64

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line[1:], " ", 2)
		if len(parts) < 2 {
			continue
		}
		vertexID := parts[0]
		rest := strings.TrimSpace(parts[1])

		if strings.HasPrefix(rest, "[") || strings.Contains(rest, "exporting cache") {
			stepDescriptions[vertexID] = rest
		} else if rest == "CACHED" || strings.HasPrefix(rest, "CACHED ") {
			if description, ok := stepDescriptions[vertexID]; ok {
				cachedSteps[description] = true
			}
		} else if strings.HasPrefix(rest, "DONE ") {
			fields := strings.Fields(rest)
			if len(fields) >= 2 {
				duration, parseError := time.ParseDuration(fields[1])
				if parseError != nil {
					continue
				}
				if description, ok := stepDescriptions[vertexID]; ok {
					if strings.Contains(description, "exporting cache") {
						exportDuration = duration.Seconds()
					} else if strings.HasPrefix(description, "[") && !strings.HasPrefix(description, "[internal]") && !strings.HasPrefix(description, "[auth]") && !strings.Contains(description, "FROM ") {
						stepDurations[description] = duration.Seconds()
					}
				}
			}
		}
	}

	return buildKitProgress{stepDurations: stepDurations, cachedSteps: cachedSteps, exportDuration: exportDuration}
}
