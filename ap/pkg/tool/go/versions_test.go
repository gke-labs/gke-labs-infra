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
	"os"
	"path/filepath"
	"strings"
	"testing"

	toolversions "github.com/gke-labs/gke-labs-infra/ap/pkg/tool/versions"
)

func TestVersionsYAML_Structure(t *testing.T) {
	if len(parsedGoReleases) == 0 {
		t.Fatal("no Go releases parsed from versions.yaml")
	}
	if len(parsedGoReleases) != 5 {
		t.Fatalf("expected exactly 5 releases in versions.yaml, got %d", len(parsedGoReleases))
	}

	for _, rel := range parsedGoReleases {
		if rel.Version == "" {
			t.Errorf("empty Go version in release")
		}
		if len(rel.Assets) != 5 {
			t.Errorf("Go release %s has %d assets, want 5", rel.Version, len(rel.Assets))
		}
		for _, plat := range supportedPlatforms {
			digest, ok := rel.Assets[plat]
			if !ok || digest == "" {
				t.Errorf("Go release %s missing asset for platform %s", rel.Version, plat)
				continue
			}
			if !strings.HasPrefix(digest, "sha256:") {
				t.Errorf("Go release %s asset %s digest does not have sha256: prefix: %s", rel.Version, plat, digest)
			}
			if len(digest) != len("sha256:")+64 {
				t.Errorf("Go release %s asset %s digest length is invalid: %s", rel.Version, plat, digest)
			}
		}
	}
}

func TestLookupGo(t *testing.T) {
	rel, ok := LookupGo("go1.27.3")
	if !ok || rel == nil {
		t.Fatal("expected to find go1.27.3")
	}
	if rel.Version != "go1.27.3" {
		t.Errorf("expected version go1.27.3, got %s", rel.Version)
	}

	relNoPrefix, okNoPrefix := LookupGo("1.27.3")
	if !okNoPrefix || relNoPrefix == nil {
		t.Fatal("expected to find 1.27.3")
	}

	relV, okV := LookupGo("v1.27.3")
	if !okV || relV == nil {
		t.Fatal("expected to find v1.27.3")
	}

	_, okNone := LookupGo("go0.0.0-nonexistent")
	if okNone {
		t.Error("expected LookupGo for nonexistent version to return false")
	}
}

func TestVersionsYAML_Sorted(t *testing.T) {
	var prev string
	for i, rel := range parsedGoReleases {
		if i > 0 {
			curr := rel.Version
			if toolversions.Compare(prev, curr) <= 0 {
				t.Errorf("Go versions table is not sorted newest-first: %s is not newer than %s", prev, curr)
			}
		}
		prev = rel.Version
	}
}

// TestNoGoGenerateDirective verifies that no //go:generate directive exists under
// ap/pkg/tool/go so that ap generate does not automatically run update-versions in CI.
func TestNoGoGenerateDirective(t *testing.T) {
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for lineNum, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//go:generate") {
				t.Errorf("%s:%d has //go:generate directive: %s", path, lineNum+1, trimmed)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed walking directory: %v", err)
	}
}
