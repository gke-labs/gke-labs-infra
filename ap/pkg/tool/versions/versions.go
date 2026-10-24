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

package versions

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Release represents a known tool release and its per-platform asset digests.
type Release struct {
	Version string            `yaml:"version"`
	Assets  map[string]string `yaml:"assets"`
}

// Parse parses and strictly decodes the given section from YAML data into []Release.
func Parse(data []byte, section string) ([]Release, error) {
	var raw map[string]yaml.Node
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse yaml: %w", err)
	}

	secNode, ok := raw[section]
	if !ok {
		return nil, fmt.Errorf("section %q not found in versions data", section)
	}

	buf, err := yaml.Marshal(&secNode)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal section %q: %w", section, err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(buf))
	dec.KnownFields(true)
	var releases []Release
	if err := dec.Decode(&releases); err != nil {
		return nil, fmt.Errorf("failed to decode releases for section %q: %w", section, err)
	}

	return releases, nil
}

// Latest returns the newest known release from the slice, or nil if empty.
func Latest(releases []Release) *Release {
	if len(releases) == 0 {
		return nil
	}
	return &releases[0]
}

// Lookup finds a release by version string (with or without 'v' or 'go' prefix).
func Lookup(releases []Release, version string) (*Release, bool) {
	norm := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(version), "go"), "v")
	for i := range releases {
		relNorm := strings.TrimPrefix(strings.TrimPrefix(releases[i].Version, "go"), "v")
		if relNorm == norm {
			return &releases[i], true
		}
	}
	return nil, false
}

// Compare compares two semantic versions. It returns 1 if a > b, -1 if a < b, and 0 if a == b.
func Compare(a, b string) int {
	parts1 := parseVersionParts(a)
	parts2 := parseVersionParts(b)

	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := 0; i < maxLen; i++ {
		p1 := 0
		if i < len(parts1) {
			p1 = parts1[i]
		}
		p2 := 0
		if i < len(parts2) {
			p2 = parts2[i]
		}
		if p1 != p2 {
			if p1 > p2 {
				return 1
			}
			return -1
		}
	}
	return 0
}

func parseVersionParts(v string) []int {
	v = strings.TrimPrefix(v, "go")
	v = strings.TrimPrefix(v, "v")
	if idx := strings.IndexAny(v, "-+"); idx != -1 {
		v = v[:idx]
	}
	rawParts := strings.Split(v, ".")
	var parts []int
	for _, p := range rawParts {
		n, err := strconv.Atoi(p)
		if err != nil {
			break
		}
		parts = append(parts, n)
	}
	return parts
}

// SortNewestFirst sorts releases by version, newest first.
func SortNewestFirst(releases []Release) {
	sort.Slice(releases, func(i, j int) bool {
		return Compare(releases[i].Version, releases[j].Version) > 0
	})
}
