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

package e2e

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get current working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("failed to find repository root")
		}
		dir = parent
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func TestAstroPlugin_Build(t *testing.T) {
	if os.Getenv("RUN_E2E") == "" {
		t.Skip("RUN_E2E not set, skipping e2e test")
	}

	repoRoot := findRepoRoot(t)

	// Build ap binary for the test.
	apBin := filepath.Join(t.TempDir(), "ap")
	buildCmd := exec.Command("go", "build", "-o", apBin, "./ap")
	buildCmd.Dir = repoRoot
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build ap binary: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	testRepo := t.TempDir()

	// Initialize minimal git repo structure.
	if err := os.MkdirAll(filepath.Join(testRepo, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(testRepo, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testRepo, ".ap", "ap.yaml"), []byte("version: '!self'\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Copy the Go module definitions and packages required to build the plugin entrypoint.
	if err := copyFile(filepath.Join(repoRoot, "go.mod"), filepath.Join(testRepo, "go.mod")); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(filepath.Join(repoRoot, "go.sum"), filepath.Join(testRepo, "go.sum")); err != nil {
		t.Fatal(err)
	}
	if err := copyDir(filepath.Join(repoRoot, "ap", "pkg"), filepath.Join(testRepo, "ap", "pkg")); err != nil {
		t.Fatalf("failed to copy ap/pkg: %v", err)
	}

	// Copy the plugin Dockerfile and assets from this repository.
	pluginSourceDir := filepath.Join(repoRoot, "ap", "images", "ap-plugin-astro")
	pluginTargetDir := filepath.Join(testRepo, "ap", "images", "ap-plugin-astro")
	if err := copyDir(pluginSourceDir, pluginTargetDir); err != nil {
		t.Fatalf("failed to copy astro plugin image definition: %v", err)
	}

	// Declare the plugin in .ap/plugins.yaml using dockerfile:
	pluginsConfig := `
plugins:
  astro:
    dockerfile: ap/images/ap-plugin-astro/Dockerfile
`
	if err := os.WriteFile(filepath.Join(testRepo, ".ap", "plugins.yaml"), []byte(pluginsConfig), 0644); err != nil {
		t.Fatal(err)
	}

	// Create an Astro site in docs/
	docsDir := filepath.Join(testRepo, "docs")
	if err := os.MkdirAll(filepath.Join(docsDir, "src", "pages"), 0755); err != nil {
		t.Fatal(err)
	}
	astroConfig := `export default {};`
	if err := os.WriteFile(filepath.Join(docsDir, "astro.config.mjs"), []byte(astroConfig), 0644); err != nil {
		t.Fatal(err)
	}
	indexPage := `---
---
<!DOCTYPE html>
<html>
<head><title>Astro Plugin Test</title></head>
<body><h1>Hello from ap Astro plugin!</h1></body>
</html>
`
	if err := os.WriteFile(filepath.Join(docsDir, "src", "pages", "index.astro"), []byte(indexPage), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a second Astro site in site/ nested directory to verify multi-site activation.
	siteDir := filepath.Join(testRepo, "site")
	if err := os.MkdirAll(filepath.Join(siteDir, "src", "pages"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(siteDir, "astro.config.mjs"), []byte(astroConfig), 0644); err != nil {
		t.Fatal(err)
	}
	sitePage := `---
---
<!DOCTYPE html>
<html>
<head><title>Second Site</title></head>
<body><h1>Second Astro Site</h1></body>
</html>
`
	if err := os.WriteFile(filepath.Join(siteDir, "src", "pages", "index.astro"), []byte(sitePage), 0644); err != nil {
		t.Fatal(err)
	}

	// Run "ap build" in testRepo.
	var stdout, stderr bytes.Buffer
	apCmd := exec.CommandContext(t.Context(), apBin, "build")
	apCmd.Dir = testRepo
	apCmd.Env = os.Environ()
	apCmd.Stdout = &stdout
	apCmd.Stderr = &stderr

	if err := apCmd.Run(); err != nil {
		t.Fatalf("ap build failed: %v\nStdout: %s\nStderr: %s", err, stdout.String(), stderr.String())
	}

	// Verify docs/dist/index.html was produced and contains expected content.
	docsOutput := filepath.Join(docsDir, "dist", "index.html")
	docsContent, err := os.ReadFile(docsOutput)
	if err != nil {
		t.Fatalf("expected output file %s: %v", docsOutput, err)
	}
	if !strings.Contains(string(docsContent), "Hello from ap Astro plugin!") {
		t.Errorf("expected docs/dist/index.html to contain 'Hello from ap Astro plugin!', got: %s", string(docsContent))
	}

	// Verify site/dist/index.html was produced for the second site.
	siteOutput := filepath.Join(siteDir, "dist", "index.html")
	siteContent, err := os.ReadFile(siteOutput)
	if err != nil {
		t.Fatalf("expected output file %s: %v", siteOutput, err)
	}
	if !strings.Contains(string(siteContent), "Second Astro Site") {
		t.Errorf("expected site/dist/index.html to contain 'Second Astro Site', got: %s", string(siteContent))
	}
}
