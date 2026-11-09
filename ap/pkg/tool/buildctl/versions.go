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
	_ "embed"
	"fmt"
	"strings"

	toolversions "github.com/gke-labs/gke-labs-infra/ap/pkg/tool/versions"
)

//go:embed versions.yaml
var embeddedVersionsYAML []byte

var supportedPlatforms = []string{
	"linux-amd64",
	"linux-arm64",
	"darwin-amd64",
	"darwin-arm64",
	"windows-amd64",
}

var (
	parsedBuildctlReleases = func() []toolversions.Release {
		releases, err := toolversions.Parse(embeddedVersionsYAML, "buildctl")
		if err != nil {
			panic(fmt.Sprintf("failed to parse embedded versions.yaml buildctl: %v", err))
		}
		return releases
	}()
)

// LatestBuildctl returns the newest known buildctl release from the table, or nil if empty.
func LatestBuildctl() *toolversions.Release {
	return toolversions.Latest(parsedBuildctlReleases)
}

// LookupBuildctl finds a buildctl release by version string (with or without 'v' prefix).
func LookupBuildctl(version string) (*toolversions.Release, bool) {
	return toolversions.Lookup(parsedBuildctlReleases, version)
}

// NormalizeTag ensures that the version string has a leading "v".
func NormalizeTag(version string) string {
	v := strings.TrimSpace(version)
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}

// ResolvePlatform maps runtime.GOOS and runtime.GOARCH to the supported platform identifier.
func ResolvePlatform(goos, goarch string) (string, error) {
	switch goos {
	case "linux":
		switch goarch {
		case "amd64", "arm64":
			return goos + "-" + goarch, nil
		default:
			return "", fmt.Errorf("unsupported linux architecture: %s", goarch)
		}
	case "darwin":
		switch goarch {
		case "amd64", "arm64":
			return goos + "-" + goarch, nil
		default:
			return "", fmt.Errorf("unsupported darwin architecture: %s", goarch)
		}
	case "windows":
		switch goarch {
		case "amd64":
			return goos + "-" + goarch, nil
		default:
			return "", fmt.Errorf("unsupported windows architecture: %s", goarch)
		}
	default:
		return "", fmt.Errorf("unsupported OS: %s", goos)
	}
}

// ResolveBuildctlAsset returns the GitHub release asset archive name for the given version, OS, and architecture.
func ResolveBuildctlAsset(tag, goos, goarch string) (string, error) {
	plat, err := ResolvePlatform(goos, goarch)
	if err != nil {
		return "", err
	}
	normTag := NormalizeTag(tag)
	return fmt.Sprintf("buildkit-%s.%s.tar.gz", normTag, plat), nil
}

// ResolveBuildctlDigest looks up the SHA-256 digest for the given platform in a release.
func ResolveBuildctlDigest(rel *toolversions.Release, goos, goarch string) (string, error) {
	plat, err := ResolvePlatform(goos, goarch)
	if err != nil {
		return "", err
	}
	if rel.Assets != nil {
		if digest, ok := rel.Assets[plat]; ok && digest != "" {
			return digest, nil
		}
		assetName, err := ResolveBuildctlAsset(rel.Version, goos, goarch)
		if err != nil {
			return "", err
		}
		if digest, ok := rel.Assets[assetName]; ok && digest != "" {
			return digest, nil
		}
	}
	return "", fmt.Errorf("buildctl %s has no asset for platform %s in versions.yaml", rel.Version, plat)
}

// ResolveBuildctlDownloadURL computes the download URL for buildkit release archives.
func ResolveBuildctlDownloadURL(baseURL, tag, goos, goarch string) (string, error) {
	normTag := NormalizeTag(tag)
	assetName, err := ResolveBuildctlAsset(normTag, goos, goarch)
	if err != nil {
		return "", err
	}
	if baseURL == "" {
		baseURL = fmt.Sprintf("https://github.com/moby/buildkit/releases/download/%s", normTag)
	}
	return fmt.Sprintf("%s/%s", strings.TrimRight(baseURL, "/"), assetName), nil
}
