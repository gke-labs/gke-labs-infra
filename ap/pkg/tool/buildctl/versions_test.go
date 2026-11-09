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
	"os"
	"path/filepath"
	"strings"
	"testing"

	toolversions "github.com/gke-labs/gke-labs-infra/ap/pkg/tool/versions"
)

func TestVersionsYAML_Structure(t *testing.T) {
	if len(parsedBuildctlReleases) == 0 {
		t.Fatal("no buildctl releases parsed from versions.yaml")
	}
	if len(parsedBuildctlReleases) != 5 {
		t.Fatalf("expected exactly 5 releases in versions.yaml, got %d", len(parsedBuildctlReleases))
	}

	for _, rel := range parsedBuildctlReleases {
		if rel.Version == "" {
			t.Errorf("empty buildctl version in release")
		}
		if len(rel.Assets) != 5 {
			t.Errorf("buildctl release %s has %d assets, want 5", rel.Version, len(rel.Assets))
		}
		for _, plat := range supportedPlatforms {
			digest, ok := rel.Assets[plat]
			if !ok || digest == "" {
				t.Errorf("buildctl release %s missing asset %s", rel.Version, plat)
				continue
			}
			if !strings.HasPrefix(digest, "sha256:") {
				t.Errorf("buildctl release %s asset %s digest does not have sha256: prefix: %s", rel.Version, plat, digest)
			}
			if len(digest) != len("sha256:")+64 {
				t.Errorf("buildctl release %s asset %s digest length is invalid: %s", rel.Version, plat, digest)
			}
		}
	}
}

func TestLookupBuildctl(t *testing.T) {
	rel, ok := LookupBuildctl("v0.34.2")
	if !ok || rel == nil {
		t.Fatal("expected to find buildctl v0.34.2")
	}
	if rel.Version != "v0.34.2" {
		t.Errorf("expected version v0.34.2, got %s", rel.Version)
	}

	relNoV, okNoV := LookupBuildctl("0.34.2")
	if !okNoV || relNoV == nil {
		t.Fatal("expected to find buildctl 0.34.2")
	}

	_, okNone := LookupBuildctl("v0.0.0-nonexistent")
	if okNone {
		t.Error("expected LookupBuildctl for nonexistent version to return false")
	}
}

func TestVersionsYAML_Sorted(t *testing.T) {
	var prevBuildctl string
	for i, rel := range parsedBuildctlReleases {
		if i > 0 {
			curr := rel.Version
			if toolversions.Compare(prevBuildctl, curr) <= 0 {
				t.Errorf("buildctl versions table is not sorted newest-first: %s is not newer than %s", prevBuildctl, curr)
			}
		}
		prevBuildctl = rel.Version
	}
}

// TestNoGoGenerateDirective verifies that no //go:generate directive exists under
// ap/pkg/tool/buildctl so that ap generate does not automatically run update-versions in CI.
func TestNoGoGenerateDirective(t *testing.T) {
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".go") {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		lines := strings.Split(string(content), "\n")
		for lineNum, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//go:generate") {
				t.Errorf("%s:%d contains unexpected //go:generate directive: %s (update-versions must only be run manually or by scheduled jobs, never on every ap generate / CI run)", path, lineNum+1, trimmed)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to walk package directory: %v", err)
	}
}
