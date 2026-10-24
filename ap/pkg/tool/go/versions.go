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
	parsedGoReleases = func() []toolversions.Release {
		rels, err := toolversions.Parse(embeddedVersionsYAML, "go")
		if err != nil {
			panic(fmt.Sprintf("failed to parse embedded versions.yaml go: %v", err))
		}
		return rels
	}()
)

// LatestGo returns the newest known Go release from the table, or nil if empty.
func LatestGo() *toolversions.Release {
	return toolversions.Latest(parsedGoReleases)
}

// LookupGo finds a Go release by version string (with or without 'go' or 'v' prefix).
func LookupGo(version string) (*toolversions.Release, bool) {
	return toolversions.Lookup(parsedGoReleases, version)
}

// NormalizeVersion ensures a Go version string has the canonical "goX.Y.Z" prefix.
func NormalizeVersion(version string) string {
	v := strings.TrimSpace(version)
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "go") {
		return "go" + v
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
