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

package docker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/fetch"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/github"
	toolversions "github.com/gke-labs/gke-labs-infra/ap/pkg/tool/versions"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"
)

const maxReleases = 5

type dockerPlatformListing struct {
	platform string
	goos     string
	goarch   string
	osPart   string
	archPart string
	ext      string
}

var dockerListingPlatforms = []dockerPlatformListing{
	{platform: "linux-amd64", goos: "linux", goarch: "amd64", osPart: "linux", archPart: "x86_64", ext: ".tgz"},
	{platform: "linux-arm64", goos: "linux", goarch: "arm64", osPart: "linux", archPart: "aarch64", ext: ".tgz"},
	{platform: "darwin-amd64", goos: "darwin", goarch: "amd64", osPart: "mac", archPart: "x86_64", ext: ".tgz"},
	{platform: "darwin-arm64", goos: "darwin", goarch: "arm64", osPart: "mac", archPart: "aarch64", ext: ".tgz"},
	{platform: "windows-amd64", goos: "windows", goarch: "amd64", osPart: "win", archPart: "x86_64", ext: ".zip"},
}

type versionsData struct {
	Docker []toolversions.Release `yaml:"docker"`
	Buildx []toolversions.Release `yaml:"buildx"`
}

// UpdateOptions specifies which sections to update in ap/pkg/tool/docker/versions.yaml.
type UpdateOptions struct {
	Docker bool
	Buildx bool
}

// Update updates ap/pkg/tool/docker/versions.yaml with the specified options.
// If opts is omitted, it defaults to updating both docker and buildx.
func Update(ctx context.Context, repoRoot string, opts ...UpdateOptions) error {
	opt := UpdateOptions{
		Docker: true,
		Buildx: true,
	}
	if len(opts) > 0 {
		opt = opts[0]
	}

	if !opt.Docker && !opt.Buildx {
		return nil
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, github.APITimeout)
		defer cancel()
	}

	targetPath := filepath.Join(repoRoot, "ap", "pkg", "tool", "docker", "versions.yaml")

	var dockerReleases []toolversions.Release
	var buildxReleases []toolversions.Release

	if opt.Docker {
		rels, err := fetchDockerReleases(ctx)
		if err != nil {
			return fmt.Errorf("failed to fetch docker releases: %w", err)
		}
		dockerReleases = rels
	} else {
		existingDocker, err := loadExistingSection(targetPath, "docker", parsedDockerReleases)
		if err != nil {
			return err
		}
		dockerReleases = existingDocker
	}

	if opt.Buildx {
		rels, err := fetchBuildxReleases(ctx)
		if err != nil {
			return fmt.Errorf("failed to fetch buildx releases: %w", err)
		}
		buildxReleases = rels
	} else {
		existingBuildx, err := loadExistingSection(targetPath, "buildx", parsedBuildxReleases)
		if err != nil {
			return err
		}
		buildxReleases = existingBuildx
	}

	data, err := yaml.Marshal(&versionsData{
		Docker: dockerReleases,
		Buildx: buildxReleases,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal versions data: %w", err)
	}

	if err := fileutils.WriteFileAtomic(ctx, targetPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", targetPath, err)
	}

	return nil
}

// UpdateVersionsFile updates both docker and buildx releases in versions.yaml.
func UpdateVersionsFile(ctx context.Context, repoRoot string) error {
	return Update(ctx, repoRoot, UpdateOptions{Docker: true, Buildx: true})
}

func loadExistingSection(path, section string, fallback []toolversions.Release) ([]toolversions.Release, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fallback, nil
		}
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	rels, err := toolversions.Parse(data, section)
	if err != nil {
		return nil, fmt.Errorf("failed to parse existing %s section from %s: %w", section, path, err)
	}
	return rels, nil
}

func fetchDockerReleases(ctx context.Context) ([]toolversions.Release, error) {
	baseURL := os.Getenv("DOCKER_DOWNLOAD_BASE_URL")
	if baseURL == "" {
		baseURL = "https://download.docker.com"
	}

	versionsPerPlatform := make([]map[string]bool, len(dockerListingPlatforms))

	for i, p := range dockerListingPlatforms {
		listingURL := fmt.Sprintf("%s/%s/static/stable/%s/", strings.TrimRight(baseURL, "/"), p.osPart, p.archPart)
		vers, err := fetchPlatformVersions(ctx, listingURL, p.ext)
		if err != nil {
			return nil, fmt.Errorf("failed listing %s: %w", listingURL, err)
		}
		vMap := make(map[string]bool)
		for _, v := range vers {
			vMap[v] = true
		}
		versionsPerPlatform[i] = vMap
	}

	var commonVersions []string
	for v := range versionsPerPlatform[0] {
		presentInAll := true
		for p := 1; p < len(dockerListingPlatforms); p++ {
			if !versionsPerPlatform[p][v] {
				presentInAll = false
				break
			}
		}
		if presentInAll {
			commonVersions = append(commonVersions, v)
		}
	}

	sort.Slice(commonVersions, func(i, j int) bool {
		return toolversions.Compare(commonVersions[i], commonVersions[j]) > 0
	})

	if len(commonVersions) > maxReleases {
		commonVersions = commonVersions[:maxReleases]
	}

	var releases []toolversions.Release
	for _, ver := range commonVersions {
		assetsMap := make(map[string]string)
		for _, p := range dockerListingPlatforms {
			assetURL, err := ResolveDockerDownloadURL(baseURL, ver, p.goos, p.goarch)
			if err != nil {
				return nil, err
			}
			digest, err := fetch.Digest(ctx, assetURL)
			if err != nil {
				return nil, fmt.Errorf("failed computing sha256 for %s (%s): %w", ver, p.platform, err)
			}
			assetsMap[p.platform] = digest
		}
		releases = append(releases, toolversions.Release{
			Version: ver,
			Assets:  assetsMap,
		})
	}

	return releases, nil
}

func fetchPlatformVersions(ctx context.Context, url, ext string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "gke-labs-ap")

	resp, err := scoped.HTTPClient(ctx).Do(req)
	if err != nil {
		return nil, err
	}
	defer github.CloseResponseBody(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP status %s", resp.Status)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return parsePlatformVersions(bodyBytes, ext), nil
}

func parsePlatformVersions(body []byte, ext string) []string {
	var vers []string
	seen := make(map[string]bool)

	content := string(body)
	for {
		idx := strings.Index(content, "href=")
		if idx == -1 {
			break
		}
		content = content[idx+len("href="):]
		if len(content) == 0 {
			break
		}

		quote := content[0]
		if quote != '"' && quote != '\'' {
			continue
		}
		content = content[1:]

		endQuote := strings.IndexByte(content, quote)
		if endQuote == -1 {
			break
		}
		link := content[:endQuote]
		content = content[endQuote+1:]

		link = strings.TrimSpace(link)
		if cutIdx := strings.IndexAny(link, "?#"); cutIdx != -1 {
			link = link[:cutIdx]
		}
		filename := filepath.Base(link)
		if !strings.HasPrefix(filename, "docker-") || !strings.HasSuffix(filename, ext) {
			continue
		}
		if strings.HasPrefix(filename, "docker-rootless-") || strings.HasPrefix(filename, "docker-buildx-") {
			continue
		}
		ver := filename[len("docker-") : len(filename)-len(ext)]
		if ver == "" || ver[0] < '0' || ver[0] > '9' {
			continue
		}
		if strings.Contains(ver, "-rc") || strings.Contains(ver, "-beta") || strings.Contains(ver, "-alpha") || strings.Contains(ver, "-test") || strings.Contains(ver, "-tp") {
			continue
		}
		if !seen[ver] {
			seen[ver] = true
			vers = append(vers, ver)
		}
	}

	return vers
}

func fetchBuildxReleases(ctx context.Context) ([]toolversions.Release, error) {
	apiBase := os.Getenv("BUILDX_GITHUB_API_BASE_URL")
	if apiBase == "" {
		apiBase = os.Getenv("GITHUB_API_BASE_URL")
	}
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}

	// Fetch up to maxReleases*2 candidates to inspect, enough to yield maxReleases
	// after filtering out drafts, prereleases, and releases missing platform assets.
	rawReleases, err := github.ListReleases(ctx, apiBase, "docker/buildx", maxReleases*2)
	if err != nil {
		return nil, err
	}

	var allReleases []toolversions.Release
	seenVersions := make(map[string]bool)

	for _, rel := range rawReleases {
		if rel.Draft || rel.Prerelease {
			continue
		}
		ver := strings.TrimPrefix(rel.TagName, "v")
		if ver == "" || ver[0] < '0' || ver[0] > '9' || strings.Contains(ver, "-") {
			continue
		}
		if seenVersions[ver] {
			continue
		}

		checksumsMap := make(map[string]string)
		for _, a := range rel.Assets {
			if strings.HasPrefix(a.Name, "checksums") && strings.HasSuffix(a.Name, ".txt") {
				content, err := github.GetText(ctx, a.BrowserDownloadURL)
				if err != nil {
					klog.V(2).Infof("failed to fetch buildx checksum asset %s: %v", a.BrowserDownloadURL, err)
					continue
				}
				for _, line := range strings.Split(content, "\n") {
					fields := strings.Fields(line)
					if len(fields) >= 2 {
						h := fields[0]
						fn := strings.TrimPrefix(fields[1], "*")
						checksumsMap[fn] = h
					}
				}
			}
		}

		expectedFilenames := map[string]string{
			"linux-amd64":   fmt.Sprintf("buildx-v%s.linux-amd64", ver),
			"linux-arm64":   fmt.Sprintf("buildx-v%s.linux-arm64", ver),
			"darwin-amd64":  fmt.Sprintf("buildx-v%s.darwin-amd64", ver),
			"darwin-arm64":  fmt.Sprintf("buildx-v%s.darwin-arm64", ver),
			"windows-amd64": fmt.Sprintf("buildx-v%s.windows-amd64.exe", ver),
		}

		assetsMap := make(map[string]string)
		for _, plat := range supportedPlatforms {
			fn := expectedFilenames[plat]
			if digest, ok := checksumsMap[fn]; ok && digest != "" {
				assetsMap[plat] = "sha256:" + digest
				continue
			}

			if a, ok := github.FindAsset(rel.Assets, fn); ok && a.BrowserDownloadURL != "" {
				d, err := fetch.Digest(ctx, a.BrowserDownloadURL)
				if err == nil && d != "" {
					assetsMap[plat] = d
				}
			}
		}

		if len(assetsMap) == len(supportedPlatforms) {
			allReleases = append(allReleases, toolversions.Release{
				Version: ver,
				Assets:  assetsMap,
			})
			seenVersions[ver] = true
		}
	}

	toolversions.SortNewestFirst(allReleases)

	if len(allReleases) > maxReleases {
		allReleases = allReleases[:maxReleases]
	}

	return allReleases, nil
}
