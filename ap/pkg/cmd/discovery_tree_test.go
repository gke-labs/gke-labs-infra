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

package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/walker"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/format"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/generate"
	golang "github.com/gke-labs/gke-labs-infra/ap/pkg/go"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/images"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/k8s"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

func TestSingleWalkPerInvocation(t *testing.T) {
	sourcetree.ResetCache()
	tempDir := t.TempDir()

	// Create repository tree
	files := map[string]string{
		".git/config":                 "",
		".ap/go.yaml":                 "skip:\n  - skipped/**\n",
		"go.mod":                      "module example.com/root\n\ngo 1.26\n",
		"main.go":                     "package main\n\nfunc main() {}\n",
		"main_test.go":                "package main\n\nimport \"testing\"\nfunc TestMain(t *testing.T) {}\n",
		"images/root-app/Dockerfile":  "FROM alpine\n",
		"k8s/root.yaml":               "apiVersion: v1\nkind: ConfigMap\n",
		"tests/e2e/e2e_test.go":       "package e2e\n\nimport \"testing\"\nfunc TestE2E(t *testing.T) {}\n",
		"sub/.ap/ap.yaml":             "",
		"sub/go.mod":                  "module example.com/root/sub\n\ngo 1.26\n",
		"sub/sub.go":                  "package sub\n",
		"sub/images/sub/Dockerfile":   "FROM alpine\n",
		"sub/k8s/sub.yaml":            "apiVersion: v1\nkind: Pod\n",
		"skipped/.ap/ap.yaml":         "",
		"skipped/go.mod":              "module example.com/root/skipped\n\ngo 1.26\n",
		"skipped/skip.go":             "package skipped\n",
		"skipped/skip_test.go":        "package skipped\n",
		"skipped/images/s/Dockerfile": "FROM alpine\n",
		"skipped/k8s/skip.yaml":       "apiVersion: v1\nkind: Pod\n",
	}

	for rel, content := range files {
		full := filepath.Join(tempDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("MkdirAll failed: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile failed: %v", err)
		}
	}

	// Step 1: Initialize RootOptions and Resolve targets
	rootOpt := RootOptions{
		RepoRoot: tempDir,
	}

	if err := rootOpt.Resolve([]string{"//..."}); err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// At this point, the repository tree was walked once during Resolve
	if count := sourcetree.WalkCount(); count != 1 {
		t.Fatalf("expected WalkCount=1 after Resolve, got %d", count)
	}

	r, err := rootOpt.GetSourceTree()
	if err != nil {
		t.Fatalf("GetSourceTree failed: %v", err)
	}

	// Step 2: Run DiscoverScopes with the shared repo
	scopes, err := DiscoverScopes(r, rootOpt.RepoRoot, rootOpt.Dir)
	if err != nil {
		t.Fatalf("DiscoverScopes failed: %v", err)
	}

	// Verify no additional walks happened
	if count := sourcetree.WalkCount(); count != 1 {
		t.Fatalf("expected WalkCount=1 after DiscoverScopes, got %d", count)
	}

	// Step 3: Run all task discovery functions using scopes and shared tree
	for _, scope := range scopes {
		naming := images.ResolveImageNaming(nil)
		if _, err := images.BuildTasks(scope.SourceTree(), scope.Dir(), false, "", naming); err != nil {
			t.Fatalf("BuildTasks failed for %s: %v", scope.Dir(), err)
		}
		if _, err := k8s.DeployTasks(scope.SourceTree(), scope.Dir()); err != nil {
			t.Fatalf("DeployTasks failed for %s: %v", scope.Dir(), err)
		}
		if _, err := golang.LintTasks(scope.SourceTree(), scope.Dir()); err != nil {
			t.Fatalf("LintTasks failed for %s: %v", scope.Dir(), err)
		}
		if _, err := golang.TestTasks(scope.SourceTree(), scope.Dir()); err != nil {
			t.Fatalf("TestTasks failed for %s: %v", scope.Dir(), err)
		}
		if _, err := format.FormatTasks(scope.SourceTree(), scope.Dir()); err != nil {
			t.Fatalf("FormatTasks failed for %s: %v", scope.Dir(), err)
		}
		if _, err := tasks.FindTaskScripts(scope.SourceTree(), scope.Dir()); err != nil {
			t.Fatalf("FindTaskScripts failed for %s: %v", scope.Dir(), err)
		}
	}

	if _, err := generate.GenerateTasks(rootOpt.RepoRoot, scopes); err != nil {
		t.Fatalf("GenerateTasks failed: %v", err)
	}

	// Verify that still only exactly ONE walk occurred throughout the entire lifecycle
	if count := sourcetree.WalkCount(); count != 1 {
		t.Fatalf("expected WalkCount to remain 1 after all discovery, got %d", count)
	}
}

func TestSkipUniformlyInvisibleToAllConsumers(t *testing.T) {
	sourcetree.ResetCache()
	tempDir := t.TempDir()

	files := map[string]string{
		".git/config":                     "",
		".ap/go.yaml":                     "skip:\n  - ignored/**\n  - dev/tasks/skipped-*\n",
		"go.mod":                          "module example.com/root\n\ngo 1.26\n",
		"main.go":                         "package main\n\nfunc main() {}\n",
		"images/root/Dockerfile":          "FROM alpine\n",
		"k8s/root.yaml":                   "apiVersion: v1\nkind: ConfigMap\n",
		"dev/tasks/build-valid":           "#!/bin/bash\n",
		"dev/tasks/skipped-build":         "#!/bin/bash\n",
		"ignored/.ap/ap.yaml":             "",
		"ignored/go.mod":                  "module example.com/root/ignored\n\ngo 1.26\n",
		"ignored/foo.go":                  "package ignored\n",
		"ignored/foo_test.go":             "package ignored\n\nimport \"testing\"\nfunc TestFoo(t *testing.T) {}\n",
		"ignored/images/Dockerfile":       "FROM alpine\n",
		"ignored/k8s/ignored.yaml":        "apiVersion: v1\nkind: Pod\n",
		"ignored/dev/tasks/build-ignored": "#!/bin/bash\n",
	}

	for rel, content := range files {
		full := filepath.Join(tempDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("MkdirAll failed: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile failed: %v", err)
		}
	}

	cfg, err := config.Load(tempDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}

	r, err := sourcetree.Open(tempDir, sourcetree.WithSkip(cfg.Skip...))
	if err != nil {
		t.Fatalf("sourcetree.Open failed: %v", err)
	}

	ignoredDir := filepath.Join(tempDir, "ignored")

	// 1. DiscoverScopes must not see ignored/.ap
	scopes, err := DiscoverScopes(r, tempDir, tempDir)
	if err != nil {
		t.Fatalf("DiscoverScopes failed: %v", err)
	}
	for _, s := range scopes {
		if s.Dir() == ignoredDir {
			t.Fatalf("DiscoverScopes discovered skipped directory %s", s.Dir())
		}
	}

	// 2. ResolveTargets must not select ignored directory
	targets, err := config.ResolveTargets(r, tempDir, tempDir, []string{"./..."})
	if err != nil {
		t.Fatalf("ResolveTargets failed: %v", err)
	}
	for _, target := range targets {
		if target == ignoredDir {
			t.Fatalf("ResolveTargets returned skipped directory %s", target)
		}
	}

	// 3. Modules must not see ignored/go.mod
	for _, mod := range r.Modules() {
		if mod == ignoredDir {
			t.Fatalf("Modules returned skipped module %s", mod)
		}
	}

	// 4. FindDockerfiles must not see ignored/images/Dockerfile
	for _, df := range r.FindDockerfiles() {
		if filepath.Dir(filepath.Dir(df)) == ignoredDir || filepath.Dir(df) == ignoredDir {
			t.Fatalf("FindDockerfiles returned skipped Dockerfile %s", df)
		}
	}

	// 5. FindManifests must not see ignored/k8s/ignored.yaml
	for _, m := range r.FindManifests() {
		if filepath.Dir(filepath.Dir(m)) == ignoredDir {
			t.Fatalf("FindManifests returned skipped manifest %s", m)
		}
	}

	// 6. HasGoTests must be false for ignored
	hasTests, err := golang.HasGoTests(r, ignoredDir)
	if err != nil {
		t.Fatalf("HasGoTests failed: %v", err)
	}
	if hasTests {
		t.Fatalf("HasGoTests returned true for skipped directory %s", ignoredDir)
	}

	// 7. walker.Walk must not see files in ignored
	walkedFiles, err := walker.Walk(r, tempDir, nil, nil)
	if err != nil {
		t.Fatalf("walker.Walk failed: %v", err)
	}
	for _, f := range walkedFiles {
		rel, _ := filepath.Rel(tempDir, f)
		if filepath.ToSlash(rel) == "ignored" || len(rel) > 7 && rel[:8] == "ignored/" {
			t.Fatalf("walker.Walk returned skipped file %s", f)
		}
	}

	// 8. walker.ExpandPaths must not see files in ignored
	expandedFiles, err := walker.ExpandPaths(r, tempDir, []string{}, nil)
	if err != nil {
		t.Fatalf("walker.ExpandPaths failed: %v", err)
	}
	for _, f := range expandedFiles {
		rel, _ := filepath.Rel(tempDir, f)
		if filepath.ToSlash(rel) == "ignored" || len(rel) > 7 && rel[:8] == "ignored/" {
			t.Fatalf("walker.ExpandPaths returned skipped file %s", f)
		}
	}

	// 9. FindTaskScripts must not see dev/tasks/skipped-build or ignored/dev/tasks/build-ignored
	discoveredTasks, err := tasks.FindTaskScripts(r, tempDir)
	if err != nil {
		t.Fatalf("FindTaskScripts failed: %v", err)
	}
	foundValid := false
	for _, dt := range discoveredTasks {
		if dt.GetName() == "skipped-build" || dt.GetName() == "build-ignored" {
			t.Fatalf("FindTaskScripts discovered skipped script %s", dt.GetName())
		}
		if dt.GetName() == "build-valid" {
			foundValid = true
		}
	}
	if !foundValid {
		t.Fatalf("expected FindTaskScripts to discover build-valid")
	}

	// 10. DiscoverScopes should not include skipped-build in BuildTasks
	for _, s := range scopes {
		for _, bt := range s.BuildTasks {
			if bt.GetName() == "skipped-build" || bt.GetName() == "build-ignored" {
				t.Fatalf("scope BuildTasks included skipped task %s", bt.GetName())
			}
		}
	}
}
