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
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/github"
	toolversions "github.com/gke-labs/gke-labs-infra/ap/pkg/tool/versions"
	"gopkg.in/yaml.v3"
)

// UpdateVersionsFile fetches protoc releases from the GitHub API and plugin versions
// using 'go list -m -versions', then rewrites ap/pkg/tool/protoc/versions.yaml atomically.
func UpdateVersionsFile(ctx context.Context, repoRoot string) error {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, gitHubAPITimeout)
		defer cancel()
	}

	apiBase := os.Getenv("PROTOC_GITHUB_API_BASE_URL")
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}

	rawReleases, err := github.ListReleases(ctx, apiBase, "protocolbuffers/protobuf", 0)
	if err != nil {
		return err
	}

	var releases []toolversions.Release
	seenVersions := make(map[string]bool)

	for _, rel := range rawReleases {
		if rel.Draft || rel.Prerelease {
			continue
		}
		ver := strings.TrimPrefix(rel.TagName, "v")
		if seenVersions[ver] {
			continue
		}

		assetsMap := make(map[string]string)
		for _, plat := range supportedPlatforms {
			expectedAsset := fmt.Sprintf("protoc-%s-%s.zip", ver, plat)
			asset, found := findAsset(rel.Assets, expectedAsset)
			if !found || asset.Digest == "" {
				break
			}
			assetsMap[expectedAsset] = asset.Digest
		}

		if len(assetsMap) == len(supportedPlatforms) {
			releases = append(releases, toolversions.Release{
				Version: ver,
				Assets:  assetsMap,
			})
			seenVersions[ver] = true
		}
	}

	// Sort releases newest first
	toolversions.SortNewestFirst(releases)

	// Resolve latest plugin versions via go list
	plugins := make(map[string]string)
	pluginModules := map[string]string{
		"protoc-gen-go":      "google.golang.org/protobuf",
		"protoc-gen-go-grpc": "google.golang.org/grpc/cmd/protoc-gen-go-grpc",
	}
	pluginOrder := []string{"protoc-gen-go", "protoc-gen-go-grpc"}
	for _, pluginName := range pluginOrder {
		mod := pluginModules[pluginName]
		if mod == "" {
			continue
		}
		cmd := exec.CommandContext(ctx, "go", "list", "-m", "-versions", mod)
		cmd.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
		out, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("failed to list versions for plugin %s (%s): %w", pluginName, mod, err)
		}
		fields := strings.Fields(string(out))
		if len(fields) < 2 {
			return fmt.Errorf("no versions returned for plugin %s", pluginName)
		}

		var newest string
		for i := len(fields) - 1; i >= 1; i-- {
			v := fields[i]
			if !strings.Contains(v, "-") {
				newest = v
				break
			}
		}
		if newest == "" {
			newest = fields[len(fields)-1]
		}
		plugins[pluginName] = newest
	}

	data, err := yaml.Marshal(&versionsData{
		Protoc:  releases,
		Plugins: plugins,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal versions data: %w", err)
	}

	targetPath := filepath.Join(repoRoot, "ap", "pkg", "tool", "protoc", "versions.yaml")
	if err := fileutils.WriteFileAtomic(ctx, targetPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", targetPath, err)
	}

	return nil
}

// Update updates ap/pkg/tool/protoc/versions.yaml.
func Update(ctx context.Context, repoRoot string) error {
	return UpdateVersionsFile(ctx, repoRoot)
}
