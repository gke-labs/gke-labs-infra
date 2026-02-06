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

package walker

import (
	"path/filepath"
	"strings"
)

// segmentMatcher matches a single path segment.
type segmentMatcher interface {
	Match(segment string) bool
}

type literalMatcher string

func (m literalMatcher) Match(segment string) bool {
	return string(m) == segment
}

type globMatcher string

func (m globMatcher) Match(segment string) bool {
	matched, _ := filepath.Match(string(m), segment)
	return matched
}

type doubleStarMatcher struct{}

func (m doubleStarMatcher) Match(_ string) bool {
	return true
}

type pathMatcher struct {
	segments          []segmentMatcher
	mustBeDir         bool
	matchBasenameOnly bool
}

func (p *pathMatcher) Matches(pathSegments []string, isDir bool) bool {
	if p.mustBeDir && !isDir {
		return false
	}

	if p.matchBasenameOnly {
		if len(pathSegments) == 0 {
			return false
		}
		return p.segments[0].Match(pathSegments[len(pathSegments)-1])
	}

	return matchSegments(p.segments, pathSegments)
}

func matchSegments(pattern []segmentMatcher, path []string) bool {
	if len(pattern) == 0 {
		return len(path) == 0
	}

	first := pattern[0]
	if _, ok := first.(doubleStarMatcher); ok {
		// Optimization: if ** is the last segment, it matches everything remaining.
		if len(pattern) == 1 {
			return true
		}

		// ** matches 0 or more segments.
		// Try to match 0 segments (skip **), then 1, then 2...
		for i := 0; i <= len(path); i++ {
			if matchSegments(pattern[1:], path[i:]) {
				return true
			}
		}
		return false
	}

	if len(path) == 0 {
		return false
	}

	if !first.Match(path[0]) {
		return false
	}

	return matchSegments(pattern[1:], path[1:])
}

// IgnoreList matches paths against a list of patterns, similar to .gitignore.
type IgnoreList struct {
	matchers []*pathMatcher
}

// NewIgnoreList creates a new IgnoreList.
func NewIgnoreList(patterns []string) *IgnoreList {
	var matchers []*pathMatcher
	for _, p := range patterns {
		matchers = append(matchers, parsePattern(p))
	}
	return &IgnoreList{matchers: matchers}
}

func parsePattern(pattern string) *pathMatcher {
	mustBeDir := strings.HasSuffix(pattern, "/")
	cleanPattern := strings.TrimSuffix(pattern, "/")

	// Check for "basename only" (no slashes in the meaningful part)
	// But first handle "**/..." which is not basename only.
	// If it starts with **/, it's anchored.
	// If it contains /, it's anchored.

	isAnchored := strings.Contains(cleanPattern, "/")

	// Special case: if pattern is just "**", it matches everything?
	// gitignore says: "A leading "**" followed by a slash means match in all directories."
	// We handle ** as segments.

	parts := strings.Split(cleanPattern, "/")
	var segments []segmentMatcher
	for _, part := range parts {
		if part == "**" {
			segments = append(segments, doubleStarMatcher{})
		} else if strings.Contains(part, "*") || strings.Contains(part, "?") || strings.Contains(part, "[") {
			segments = append(segments, globMatcher(part))
		} else {
			segments = append(segments, literalMatcher(part))
		}
	}

	return &pathMatcher{
		segments:          segments,
		mustBeDir:         mustBeDir,
		matchBasenameOnly: !isAnchored,
	}
}

// ShouldIgnore returns true if the path should be ignored.
// path should be relative to the root of the walk.
func (l *IgnoreList) ShouldIgnore(path string, isDir bool) bool {
	// Normalize path to use /
	path = filepath.ToSlash(path)
	pathSegments := strings.Split(path, "/")

	for _, m := range l.matchers {
		if m.Matches(pathSegments, isDir) {
			return true
		}
	}
	return false
}
