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
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
)

type blockTransport struct {
	blockedHost string
	called      bool
}

func (b *blockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Host, b.blockedHost) {
		b.called = true
		return nil, fmt.Errorf("blocked request to %s: %s", b.blockedHost, req.URL)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func TestToolProtoc_DryRun(t *testing.T) {
	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apDir, "proto.yaml"), []byte("protoc: \"36.2\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Pre-populate cache so it doesn't need to download in dry run test
	version := "36.2"
	binDir := filepath.Join(tempCache, "tools", "protoc", version, "bin")
	includeDir := filepath.Join(tempCache, "tools", "protoc", version, "include")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(includeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "protoc"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AP_ROOT", tempDir)

	rootCmd := BuildRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)

	// Use --dry-run
	rootCmd.SetArgs([]string{"tool", "protoc", "--dry-run", "--", "--version"})

	err := rootCmd.ExecuteContext(t.Context())
	output := buf.String()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output, "[dry-run]") {
		t.Errorf("expected output to contain [dry-run], got: %s", output)
	}
	if !strings.Contains(output, "--version") {
		t.Errorf("expected output to contain --version, got: %s", output)
	}
	if !strings.Contains(output, "36.2") {
		t.Errorf("expected output to reference 36.2, got: %s", output)
	}
}

func TestToolProtoc_ColdVersion_NoRequestToGitHubAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cold execution in short mode")
	}

	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)
	tempRoot := t.TempDir()
	t.Setenv("AP_ROOT", tempRoot)
	t.Setenv("REPO_ROOT", tempRoot)

	bt := &blockTransport{blockedHost: "api.github.com"}
	client := &http.Client{Transport: bt}

	rootCmd := BuildRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"tool", "protoc", "--", "--version"})

	err := rootCmd.ExecuteContext(scoped.WithHTTPClient(t.Context(), client))
	output := buf.String()

	if err != nil {
		t.Fatalf("unexpected error running ap tool protoc -- --version: %v", err)
	}
	if bt.called {
		t.Fatal("cold ap tool protoc made a request to api.github.com")
	}
	if !strings.Contains(output, "36.2") {
		t.Errorf("expected output to contain 36.2, got: %s", output)
	}
}

func TestToolProtoc_RealExecution(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real execution in short mode")
	}

	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)
	tempRoot := t.TempDir()
	t.Setenv("AP_ROOT", tempRoot)
	t.Setenv("REPO_ROOT", tempRoot)

	rootCmd := BuildRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"tool", "protoc", "--", "--version"})

	err := rootCmd.ExecuteContext(t.Context())
	output := buf.String()

	if err != nil {
		t.Fatalf("unexpected error running ap tool protoc -- --version: %v", err)
	}
	if !strings.Contains(output, "libprotoc") && !strings.Contains(output, "protoc") {
		t.Errorf("expected protoc version output, got: %s", output)
	}
}

func TestToolUpdateVersions_Command(t *testing.T) {
	rootCmd := BuildRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"tool", "update-versions", "--help"})

	err := rootCmd.ExecuteContext(t.Context())
	if err != nil {
		t.Fatalf("unexpected error running ap tool update-versions --help: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "update-versions") {
		t.Errorf("expected help output to mention update-versions, got: %s", out)
	}
	if !strings.Contains(out, "--protoc") || !strings.Contains(out, "--docker") || !strings.Contains(out, "--buildx") || !strings.Contains(out, "--kind") || !strings.Contains(out, "--go") {
		t.Errorf("expected help output to mention --protoc, --docker, --buildx, --kind, and --go flags, got: %s", out)
	}
}

func TestToolKind_DryRun(t *testing.T) {
	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apDir, "kind.yaml"), []byte("kind: \"v0.33.0\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Pre-populate cache for kind
	kindBinDir := filepath.Join(tempCache, "tools", "kind", "v0.33.0", "bin")
	if err := os.MkdirAll(kindBinDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kindBinDir, "kind"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AP_ROOT", tempDir)

	rootCmd := BuildRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"tool", "kind", "--dry-run", "--", "version"})

	err := rootCmd.ExecuteContext(t.Context())
	output := buf.String()

	if err != nil {
		t.Fatalf("unexpected error running ap tool kind --dry-run: %v\nOutput: %s", err, output)
	}

	expectedPrefix := fmt.Sprintf("[dry-run] %s", filepath.Join(tempCache, "tools", "kind", "v0.33.0", "bin", "kind"))
	if !strings.HasPrefix(output, expectedPrefix) {
		t.Errorf("expected dry-run output to start with %q, got: %s", expectedPrefix, output)
	}
	if !strings.Contains(output, "version") {
		t.Errorf("expected output to contain 'version', got: %s", output)
	}
}

func TestToolDocker_DryRun(t *testing.T) {
	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apDir, "docker.yaml"), []byte("docker: \"28.0.1\"\nbuildx: \"v0.21.1\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Pre-populate cache for docker
	dockerBinDir := filepath.Join(tempCache, "tools", "docker", "28.0.1", "bin")
	if err := os.MkdirAll(dockerBinDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dockerBinDir, "docker"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	// Pre-populate cache for buildx
	buildxBinDir := filepath.Join(tempCache, "tools", "docker-buildx", "v0.21.1", "bin")
	if err := os.MkdirAll(buildxBinDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(buildxBinDir, "docker-buildx"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AP_ROOT", tempDir)

	rootCmd := BuildRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"tool", "docker", "--dry-run", "--", "version"})

	err := rootCmd.ExecuteContext(t.Context())
	output := buf.String()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output, "[dry-run]") {
		t.Errorf("expected output to contain [dry-run], got: %s", output)
	}
	if !strings.Contains(output, "28.0.1") {
		t.Errorf("expected output to reference 28.0.1, got: %s", output)
	}
	if !strings.Contains(output, "version") {
		t.Errorf("expected output to contain 'version', got: %s", output)
	}
}

func TestToolDockerBuildx_DryRun(t *testing.T) {
	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	tempDir := t.TempDir()
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apDir, "docker.yaml"), []byte("docker: \"28.0.1\"\nbuildx: \"v0.21.1\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Pre-populate cache for buildx
	buildxBinDir := filepath.Join(tempCache, "tools", "docker-buildx", "v0.21.1", "bin")
	if err := os.MkdirAll(buildxBinDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(buildxBinDir, "docker-buildx"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AP_ROOT", tempDir)

	rootCmd := BuildRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"tool", "docker-buildx", "--dry-run", "--", "version"})

	err := rootCmd.ExecuteContext(t.Context())
	output := buf.String()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output, "[dry-run]") {
		t.Errorf("expected output to contain [dry-run], got: %s", output)
	}
	if !strings.Contains(output, "v0.21.1") {
		t.Errorf("expected output to reference v0.21.1, got: %s", output)
	}
	if !strings.Contains(output, "version") {
		t.Errorf("expected output to contain 'version', got: %s", output)
	}
}

func TestToolDocker_DefaultNewestVersion(t *testing.T) {
	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	tempDir := t.TempDir()
	t.Setenv("AP_ROOT", tempDir)

	// Pre-populate cache for newest docker (29.9.2)
	dockerBinDir := filepath.Join(tempCache, "tools", "docker", "29.9.2", "bin")
	if err := os.MkdirAll(dockerBinDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dockerBinDir, "docker"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	rootCmd := BuildRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"tool", "docker", "--dry-run", "--", "version"})

	err := rootCmd.ExecuteContext(t.Context())
	output := buf.String()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output, "[dry-run]") {
		t.Errorf("expected output to contain [dry-run], got: %s", output)
	}
	if !strings.Contains(output, "29.9.2") {
		t.Errorf("expected output to reference newest table version 29.9.2, got: %s", output)
	}
}

func TestToolDocker_BuildxRewrite(t *testing.T) {
	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	tempDir := t.TempDir()
	t.Setenv("AP_ROOT", tempDir)

	// Pre-populate cache for buildx (0.38.1)
	buildxBinDir := filepath.Join(tempCache, "tools", "docker-buildx", "v0.38.1", "bin")
	if err := os.MkdirAll(buildxBinDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(buildxBinDir, "docker-buildx"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	rootCmd := BuildRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"tool", "docker", "--dry-run", "--", "buildx", "version"})

	err := rootCmd.ExecuteContext(t.Context())
	output := buf.String()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output, "[dry-run]") {
		t.Errorf("expected output to contain [dry-run], got: %s", output)
	}
	if !strings.Contains(output, "docker-buildx") {
		t.Errorf("expected output to reference docker-buildx binary, got: %s", output)
	}
	if !strings.Contains(output, "version") {
		t.Errorf("expected output to contain 'version', got: %s", output)
	}
	// Verify "buildx" was rewritten and stripped from the subcommand args
	if strings.Contains(output, "docker-buildx buildx version") {
		t.Errorf("expected buildx to be rewritten, but found duplicate: %s", output)
	}
}

func TestToolLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinking is not supported on Windows")
	}

	tempDir := t.TempDir()
	fakeAp := filepath.Join(t.TempDir(), "ap")
	if err := os.WriteFile(fakeAp, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AP_EXECUTABLE", fakeAp)

	rootCmd := BuildRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"tool", "link", tempDir})

	err := rootCmd.ExecuteContext(t.Context())
	if err != nil {
		t.Fatalf("unexpected error running ap tool link: %v\nOutput: %s", err, buf.String())
	}

	output := buf.String()
	expectedTools := []string{"kind", "docker", "docker-buildx", "protoc", "go"}
	for _, toolName := range expectedTools {
		linkPath := filepath.Join(tempDir, toolName)
		fi, err := os.Lstat(linkPath)
		if err != nil {
			t.Fatalf("expected symlink for %s at %s: %v", toolName, linkPath, err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("expected %s to be a symlink, got mode %v", linkPath, fi.Mode())
		}
		dest, err := os.Readlink(linkPath)
		if err != nil {
			t.Fatalf("failed to readlink for %s: %v", linkPath, err)
		}
		if dest != fakeAp {
			t.Fatalf("expected symlink %s to point to %s, got %s", linkPath, fakeAp, dest)
		}
		if !strings.Contains(output, linkPath) {
			t.Errorf("expected output to mention %s, got: %s", linkPath, output)
		}
	}

	// Test refreshing existing links
	buf.Reset()
	rootCmd.SetArgs([]string{"tool", "link", tempDir})
	if err := rootCmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("unexpected error refreshing ap tool link: %v\nOutput: %s", err, buf.String())
	}

	// Test default dir (AP_CACHE_DIR/bin)
	cacheDir := t.TempDir()
	t.Setenv("AP_CACHE_DIR", cacheDir)
	buf.Reset()
	rootCmd.SetArgs([]string{"tool", "link"})
	if err := rootCmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("unexpected error running ap tool link with default dir: %v\nOutput: %s", err, buf.String())
	}
	defaultBinDir := filepath.Join(cacheDir, "bin")
	for _, toolName := range expectedTools {
		linkPath := filepath.Join(defaultBinDir, toolName)
		dest, err := os.Readlink(linkPath)
		if err != nil {
			t.Fatalf("failed to readlink in default dir for %s: %v", toolName, err)
		}
		if dest != fakeAp {
			t.Fatalf("expected default symlink %s to point to %s, got %s", linkPath, fakeAp, dest)
		}
	}
}

func TestToolGo_DryRun(t *testing.T) {
	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	tempDir := t.TempDir()
	goMod := "module example.com/test\n\ntoolchain go1.27.3\n"
	if err := os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatal(err)
	}

	binName := "go"
	if runtime.GOOS == "windows" {
		binName = "go.exe"
	}
	goBinDir := filepath.Join(tempCache, "tools", "go", "go1.27.3", "bin")
	if err := os.MkdirAll(goBinDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goBinDir, binName), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AP_ROOT", tempDir)

	rootCmd := BuildRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"tool", "go", "--dry-run", "--", "version"})

	err := rootCmd.ExecuteContext(t.Context())
	output := buf.String()

	if err != nil {
		t.Fatalf("unexpected error running ap tool go --dry-run: %v\nOutput: %s", err, output)
	}

	expectedPrefix := fmt.Sprintf("[dry-run] %s", filepath.Join(tempCache, "tools", "go", "go1.27.3", "bin", binName))
	if !strings.HasPrefix(output, expectedPrefix) {
		t.Errorf("expected dry-run output to start with %q, got: %s", expectedPrefix, output)
	}
	if !strings.Contains(output, "version") {
		t.Errorf("expected output to contain 'version', got: %s", output)
	}
}
