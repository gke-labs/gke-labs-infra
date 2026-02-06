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

package versionbump

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gke-labs/gke-labs-infra/codestyle/pkg/walker"
	"k8s.io/klog/v2"
)

// GoVersion represents a Go version from the official downloads API.
type GoVersion struct {
	Version string `json:"version"`
	Stable  bool   `json:"stable"`
}

// Run executes the versionbump command.
func Run(ctx context.Context, root string) error {
	latestGo, err := fetchLatestGoVersion(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch latest go version: %w", err)
	}
	klog.Infof("Latest Go version: %s", latestGo)

	// Strip 'go' prefix from 'go1.25.6' -> '1.25.6'
	version := strings.TrimPrefix(latestGo, "go")

	ignore := walker.NewIgnoreList([]string{".git", "vendor", "node_modules"})

	files, err := walker.Walk(root, ignore, func(path string, _ os.FileInfo) bool {
		name := filepath.Base(path)
		return name == "go.mod" || name == "Dockerfile" || strings.HasPrefix(name, "Dockerfile.")
	})
	if err != nil {
		return fmt.Errorf("failed to walk repo: %w", err)
	}

	var errs []error
	for _, file := range files {
		if err := bumpFile(file, version); err != nil {
			errs = append(errs, fmt.Errorf("failed to bump %s: %w", file, err))
		}
	}

	return errors.Join(errs...)
}

func fetchLatestGoVersion(ctx context.Context) (string, error) {
	url := "https://go.dev/dl/?mode=json"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("unexpected status code %d fetching %s: %s", resp.StatusCode, url, string(body))
	}

	var versions []GoVersion
	if err := json.NewDecoder(resp.Body).Decode(&versions); err != nil {
		return "", fmt.Errorf("failed to decode JSON from %s: %w", url, err)
	}

	for _, v := range versions {
		if v.Stable {
			return v.Version, nil
		}
	}

	return "", fmt.Errorf("no stable go version found at %s", url)
}

var (
	goModRegex = regexp.MustCompile(`(?m)^go\s+(\d+\.\d+(?:\.\d+)?)$`)
	// In Dockerfiles, look for images like golang:1.25.6-trixie, golang:1.25-trixie, golang:1.25.6-bookworm, golang:1.25-bookworm
	dockerfileRegex = regexp.MustCompile(`golang:(\d+\.\d+(?:\.\d+)?)(-[a-z0-9]+)?`)
)

func bumpFile(path string, version string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	newContent, changed := bumpContent(filepath.Base(path), content, version)

	if changed {
		klog.Infof("Updating %s", path)
		return os.WriteFile(path, newContent, 0644)
	}

	return nil
}

func bumpContent(filename string, content []byte, version string) ([]byte, bool) {
	newContent := string(content)

	changed := false
	if filename == "go.mod" {
		if goModRegex.Match(content) {
			newContent = goModRegex.ReplaceAllString(newContent, "go "+version)
			changed = newContent != string(content)
		}
	} else if strings.Contains(filename, "Dockerfile") {
		newContent = dockerfileRegex.ReplaceAllStringFunc(newContent, func(match string) string {
			submatches := dockerfileRegex.FindStringSubmatch(match)
			if len(submatches) > 2 {
				return "golang:" + version + submatches[2]
			}
			return "golang:" + version
		})
		changed = newContent != string(content)
	}

	return []byte(newContent), changed
}
