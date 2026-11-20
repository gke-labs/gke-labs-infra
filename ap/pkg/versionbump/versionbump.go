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
	"strconv"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/walker"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/dockerfile"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/imageref"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
)

// GoVersion represents a Go version from the official downloads API.
type GoVersion struct {
	Version string `json:"version"`
	Stable  bool   `json:"stable"`
}

// Run executes the versionbump command.
func Run(ctx context.Context, r *sourcetree.SourceTree, root string, targetGoVersion string) error {
	var latestGo string
	var err error
	if targetGoVersion != "" {
		latestGo = targetGoVersion
	} else {
		latestGo, err = fetchLatestGoVersion(ctx)
		if err != nil {
			return fmt.Errorf("failed to fetch latest go version: %w", err)
		}
	}
	scoped.Log(ctx).Info("target Go version", "version", latestGo)

	// Strip 'go' prefix from 'go1.26.1' -> '1.26.1'
	version := strings.TrimPrefix(latestGo, "go")

	ignore, err := walker.NewIgnoreList([]string{".git", "vendor", "node_modules"})
	if err != nil {
		return fmt.Errorf("building ignore list: %w", err)
	}

	files, err := walker.Walk(r, root, ignore, func(path string, _ os.FileInfo) bool {
		name := filepath.Base(path)
		return name == "go.mod" || name == "Dockerfile" || strings.HasPrefix(name, "Dockerfile.")
	})
	if err != nil {
		return fmt.Errorf("failed to walk repo: %w", err)
	}

	var errs []error
	for _, file := range files {
		if err := bumpFile(ctx, file, version); err != nil {
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
	defer func() {
		if err := resp.Body.Close(); err != nil {
			scoped.Log(ctx).Warn("failed to close response body", "error", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", fmt.Errorf("unexpected status code %d fetching %s (failed to read response body: %w)", resp.StatusCode, url, err)
		}
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

func isValidVersion(s string) bool {
	if s == "" {
		return false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 2 && len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for i := 0; i < len(part); i++ {
			if part[i] < '0' || part[i] > '9' {
				return false
			}
		}
	}
	return true
}

func bumpGoMod(ctx context.Context, content string, version string) (string, bool) {
	lines := strings.Split(content, "\n")
	goLineIdx := -1
	toolchainLineIdx := -1

	for i, rawLine := range lines {
		line := strings.TrimSuffix(rawLine, "\r")
		if strings.HasPrefix(line, "go ") || strings.HasPrefix(line, "go\t") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == "go" && isValidVersion(fields[1]) {
				goLineIdx = i
			}
		} else if strings.HasPrefix(line, "toolchain ") || strings.HasPrefix(line, "toolchain\t") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == "toolchain" && strings.HasPrefix(fields[1], "go") && isValidVersion(strings.TrimPrefix(fields[1], "go")) {
				toolchainLineIdx = i
			}
		}
	}

	if goLineIdx == -1 {
		return content, false
	}

	goVersion := getGoDirectiveVersion(ctx, version)
	isCRLF := strings.Contains(content, "\r\n")

	lines[goLineIdx] = "go " + goVersion
	if isCRLF {
		lines[goLineIdx] += "\r"
	}

	if toolchainLineIdx != -1 {
		lines[toolchainLineIdx] = "toolchain go" + version
		if isCRLF {
			lines[toolchainLineIdx] += "\r"
		}
	} else {
		toolchainLine := "toolchain go" + version
		if isCRLF {
			toolchainLine += "\r"
		}
		emptyLine := ""
		if isCRLF {
			emptyLine = "\r"
		}
		newLines := make([]string, 0, len(lines)+2)
		newLines = append(newLines, lines[:goLineIdx+1]...)
		newLines = append(newLines, emptyLine, toolchainLine)
		newLines = append(newLines, lines[goLineIdx+1:]...)
		lines = newLines
	}

	newContent := strings.Join(lines, "\n")
	return newContent, newContent != content
}

func bumpDockerfile(ctx context.Context, content string, version string) (string, bool) {
	lines := strings.Split(content, "\n")
	changed := false

	froms := dockerfile.FromLines(content)
	for _, from := range froms {
		if from.Line <= 0 || from.Line > len(lines) {
			continue
		}
		line := lines[from.Line-1]

		ref, err := imageref.Parse(from.Image)
		if err != nil {
			continue
		}
		if ref.Name() != "golang" {
			continue
		}

		if ref.Digest != "" {
			scoped.Log(ctx).Warn("skipping digest-pinned image", "line", from.Line, "image", line)
			continue
		}

		i := 0
		for i < len(ref.Tag) && ((ref.Tag[i] >= '0' && ref.Tag[i] <= '9') || ref.Tag[i] == '.') {
			i++
		}
		verStr := ref.Tag[:i]
		if !isValidVersion(verStr) {
			continue
		}

		ref.Tag = version + ref.Tag[i:]
		newImage := ref.String()
		if newImage == from.Image {
			continue
		}

		newLine := strings.Replace(line, from.Image, newImage, 1)
		if newLine != line {
			lines[from.Line-1] = newLine
			changed = true
		}
	}

	newContent := strings.Join(lines, "\n")
	return newContent, changed
}

func bumpFile(ctx context.Context, path string, version string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	newContent, changed := bumpContent(ctx, filepath.Base(path), content, version)

	if changed {
		scoped.Log(ctx).Info("updating file", "path", path)
		return os.WriteFile(path, newContent, 0644)
	}

	return nil
}

func bumpContent(ctx context.Context, filename string, content []byte, version string) ([]byte, bool) {
	if filename == "go.mod" {
		newContent, changed := bumpGoMod(ctx, string(content), version)
		return []byte(newContent), changed
	} else if strings.Contains(filename, "Dockerfile") {
		newContent, changed := bumpDockerfile(ctx, string(content), version)
		return []byte(newContent), changed
	}

	return content, false
}

func getGoDirectiveVersion(ctx context.Context, version string) string {
	parts := strings.Split(version, ".")
	if len(parts) == 3 {
		patch, err := strconv.Atoi(parts[2])
		if err != nil {
			scoped.Log(ctx).Warn("failed to parse patch version", "patch", parts[2], "version", version, "error", err)
			return fmt.Sprintf("%s.%s", parts[0], parts[1])
		}
		if patch > 0 {
			return fmt.Sprintf("%s.%s.%d", parts[0], parts[1], patch-1)
		}
		return fmt.Sprintf("%s.%s", parts[0], parts[1])
	}
	return version
}
