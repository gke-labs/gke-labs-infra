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

package gotool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	toolversions "github.com/gke-labs/gke-labs-infra/ap/pkg/tool/versions"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"
)

const maxReleases = 5

type versionsData struct {
	Go []toolversions.Release `yaml:"go"`
}

type dlFile struct {
	Filename string `json:"filename"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	SHA256   string `json:"sha256"`
	Kind     string `json:"kind"`
}

type dlRelease struct {
	Version string   `json:"version"`
	Stable  bool     `json:"stable"`
	Files   []dlFile `json:"files"`
}

// Update fetches the newest Go releases from go.dev and updates
// ap/pkg/tool/go/versions.yaml with the top 5 releases and their SHA-256 digests.
func Update(ctx context.Context, repoRoot string) error {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	dlURL := os.Getenv("GO_DEV_DL_URL")
	if dlURL == "" {
		dlURL = "https://go.dev/dl/?mode=json&include=all"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request for %s: %w", dlURL, err)
	}
	req.Header.Set("User-Agent", "gke-labs-ap")

	resp, err := scoped.HTTPClient(ctx).Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch Go releases from %s: %w", dlURL, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			klog.V(2).Infof("failed to close response body: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to fetch Go releases from %s: HTTP %s", dlURL, resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response from %s: %w", dlURL, err)
	}

	var rawReleases []dlRelease
	if err := json.Unmarshal(body, &rawReleases); err != nil {
		return fmt.Errorf("failed to parse Go releases JSON: %w", err)
	}

	platforms := []struct {
		plat   string
		goos   string
		goarch string
	}{
		{"darwin-amd64", "darwin", "amd64"},
		{"darwin-arm64", "darwin", "arm64"},
		{"linux-amd64", "linux", "amd64"},
		{"linux-arm64", "linux", "arm64"},
		{"windows-amd64", "windows", "amd64"},
	}

	var releases []toolversions.Release
	seenVersions := make(map[string]bool)

	for _, rel := range rawReleases {
		if !rel.Stable || seenVersions[rel.Version] {
			continue
		}

		assetsMap := make(map[string]string)
		for _, p := range platforms {
			for _, f := range rel.Files {
				if f.Kind == "archive" && f.OS == p.goos && f.Arch == p.goarch && f.SHA256 != "" {
					assetsMap[p.plat] = "sha256:" + f.SHA256
					break
				}
			}
		}

		if len(assetsMap) == len(platforms) {
			releases = append(releases, toolversions.Release{
				Version: rel.Version,
				Assets:  assetsMap,
			})
			seenVersions[rel.Version] = true
		}
	}

	toolversions.SortNewestFirst(releases)

	if len(releases) > maxReleases {
		releases = releases[:maxReleases]
	}

	data, err := yaml.Marshal(&versionsData{
		Go: releases,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal go versions data: %w", err)
	}

	targetPath := filepath.Join(repoRoot, "ap", "pkg", "tool", "go", "versions.yaml")
	if err := fileutils.WriteFileAtomic(targetPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", targetPath, err)
	}

	return nil
}
