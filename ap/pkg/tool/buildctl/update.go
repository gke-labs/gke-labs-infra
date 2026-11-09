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

package buildctl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/fetch"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/github"
	toolversions "github.com/gke-labs/gke-labs-infra/ap/pkg/tool/versions"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"
)

const maxReleases = 5

type versionsData struct {
	Buildctl []toolversions.Release `yaml:"buildctl"`
}

// Update fetches the newest buildkit releases from the GitHub API and updates
// ap/pkg/tool/buildctl/versions.yaml with the top 5 releases.
func Update(ctx context.Context, repoRoot string) error {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, github.APITimeout)
		defer cancel()
	}

	apiBase := os.Getenv("BUILDCTL_GITHUB_API_BASE_URL")
	if apiBase == "" {
		apiBase = os.Getenv("GITHUB_API_BASE_URL")
	}
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}

	rawReleases, err := github.ListReleases(ctx, apiBase, "moby/buildkit", maxReleases*2)
	if err != nil {
		return fmt.Errorf("failed to list buildkit releases: %w", err)
	}

	var releases []toolversions.Release
	seenVersions := make(map[string]bool)

	for _, rel := range rawReleases {
		if rel.Draft || rel.Prerelease {
			continue
		}
		ver := strings.TrimPrefix(rel.TagName, "v")
		if ver == "" || ver[0] < '0' || ver[0] > '9' || strings.Contains(ver, "-") {
			continue
		}
		if seenVersions[rel.TagName] {
			continue
		}

		assetsMap := make(map[string]string)
		for _, plat := range supportedPlatforms {
			assetName := fmt.Sprintf("buildkit-%s.%s.tar.gz", rel.TagName, plat)
			asset, found := github.FindAsset(rel.Assets, assetName)
			if !found {
				break
			}

			digest := asset.Digest
			if digest == "" {
				provenanceName := fmt.Sprintf("buildkit-%s.%s.provenance.json", rel.TagName, plat)
				if provenanceAsset, ok := github.FindAsset(rel.Assets, provenanceName); ok && provenanceAsset.BrowserDownloadURL != "" {
					jsonText, err := github.GetText(ctx, provenanceAsset.BrowserDownloadURL)
					if err != nil {
						klog.V(2).Infof("failed to fetch provenance asset %s: %v", provenanceAsset.BrowserDownloadURL, err)
					} else {
						parsedDigest := extractDigestFromProvenance(jsonText, assetName)
						if parsedDigest != "" {
							digest = parsedDigest
						}
					}
				}
			}

			if digest == "" && asset.BrowserDownloadURL != "" {
				computedDigest, err := fetch.Digest(ctx, asset.BrowserDownloadURL)
				if err != nil {
					klog.V(2).Infof("failed computing digest for %s: %v", asset.BrowserDownloadURL, err)
				} else if computedDigest != "" {
					digest = computedDigest
				}
			}

			if digest == "" {
				break
			}
			assetsMap[plat] = digest
		}

		if len(assetsMap) == len(supportedPlatforms) {
			releases = append(releases, toolversions.Release{
				Version: rel.TagName,
				Assets:  assetsMap,
			})
			seenVersions[rel.TagName] = true
		}
	}

	toolversions.SortNewestFirst(releases)

	if len(releases) > maxReleases {
		releases = releases[:maxReleases]
	}

	data, err := yaml.Marshal(&versionsData{
		Buildctl: releases,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal buildctl versions data: %w", err)
	}

	targetPath := filepath.Join(repoRoot, "ap", "pkg", "tool", "buildctl", "versions.yaml")
	if err := fileutils.WriteFileAtomic(targetPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", targetPath, err)
	}

	return nil
}
