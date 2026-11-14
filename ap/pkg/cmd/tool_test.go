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
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/kubectl"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/spec"
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
	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tempDir)
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

	t.Chdir(tempDir)

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
	if err := os.MkdirAll(filepath.Join(tempRoot, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tempRoot)

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
	if err := os.MkdirAll(filepath.Join(tempRoot, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tempRoot)

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
	if !strings.Contains(out, "--protoc") || !strings.Contains(out, "--docker") || !strings.Contains(out, "--buildx") || !strings.Contains(out, "--kind") || !strings.Contains(out, "--kubectl") || !strings.Contains(out, "--kustomize") || !strings.Contains(out, "--go") {
		t.Errorf("expected help output to mention --protoc, --docker, --buildx, --kind, --kubectl, --kustomize, and --go flags, got: %s", out)
	}
}

func TestToolUpdateVersions_KubectlOnly(t *testing.T) {
	dummyHash := "c04487523e7aa7d1493f7808174c899db93d7a9cf57531420cd76b9bd415f8f4"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stable.txt":
			_, _ = fmt.Fprintln(w, "v1.37.2")
		case "/stable-1.36.txt":
			_, _ = fmt.Fprintln(w, "v1.36.6")
		default:
			if strings.HasSuffix(r.URL.Path, ".sha256") {
				_, _ = fmt.Fprintln(w, dummyHash)
				return
			}
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tempRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tempRepo, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tempRepo, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	targetDir := filepath.Join(tempRepo, "ap", "pkg", "tool", "kubectl")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}

	origSource := kubectl.Spec.Source
	kubectl.Spec.Source = &spec.KubernetesRelease{BaseURL: server.URL}
	t.Cleanup(func() {
		kubectl.Spec.Source = origSource
	})
	t.Chdir(tempRepo)

	rootCmd := BuildRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"tool", "update-versions", "--kubectl"})

	err := rootCmd.ExecuteContext(t.Context())
	if err != nil {
		t.Fatalf("unexpected error running ap tool update-versions --kubectl: %v\nOutput: %s", err, buf.String())
	}

	yamlPath := filepath.Join(targetDir, "versions.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("reading generated versions.yaml: %v", err)
	}
	if !strings.Contains(string(data), "v1.37.2") {
		t.Errorf("expected versions.yaml to contain v1.37.2, got: %s", string(data))
	}
}

func TestToolKind_DryRun(t *testing.T) {
	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	tempDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tempDir)
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

	t.Chdir(tempDir)

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

func TestToolKubectl_DryRun(t *testing.T) {
	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	tempDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tempDir)
	apDir := filepath.Join(tempDir, ".ap")
	if err := os.MkdirAll(apDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apDir, "kubectl.yaml"), []byte("kubectl: \"v1.37.2\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	binName := "kubectl"
	if runtime.GOOS == "windows" {
		binName = "kubectl.exe"
	}
	kubectlBinDir := filepath.Join(tempCache, "tools", "kubectl", "v1.37.2", "bin")
	if err := os.MkdirAll(kubectlBinDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kubectlBinDir, binName), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(tempDir)

	rootCmd := BuildRootCommand()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"tool", "kubectl", "--dry-run", "--", "version", "--client"})

	err := rootCmd.ExecuteContext(t.Context())
	output := buf.String()

	if err != nil {
		t.Fatalf("unexpected error running ap tool kubectl --dry-run: %v\nOutput: %s", err, output)
	}

	expectedPrefix := fmt.Sprintf("[dry-run] %s", filepath.Join(tempCache, "tools", "kubectl", "v1.37.2", "bin", binName))
	if !strings.HasPrefix(output, expectedPrefix) {
		t.Errorf("expected dry-run output to start with %q, got: %s", expectedPrefix, output)
	}
	if !strings.Contains(output, "version") {
		t.Errorf("expected output to contain 'version', got: %s", output)
	}
	if !strings.Contains(output, "--client") {
		t.Errorf("expected output to contain '--client', got: %s", output)
	}
}

func TestToolDocker_DryRun(t *testing.T) {
	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	tempDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tempDir)
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

	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tempDir)

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
	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tempDir)
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

	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tempDir)

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
	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tempDir)

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
	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tempDir)

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

func TestToolInstall(t *testing.T) {
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
	rootCmd.SetArgs([]string{"tool", "install", tempDir})

	err := rootCmd.ExecuteContext(t.Context())
	if err != nil {
		t.Fatalf("unexpected error running ap tool install: %v\nOutput: %s", err, buf.String())
	}

	output := buf.String()
	expectedAp := filepath.Join(tempDir, "ap")
	if fi, err := os.Stat(expectedAp); err != nil {
		t.Fatalf("expected installed ap binary at %s: %v", expectedAp, err)
	} else if fi.Mode()&0111 == 0 {
		t.Fatalf("expected installed ap at %s to be executable", expectedAp)
	}

	expectedTools := []string{"kind", "kubectl", "kustomize", "docker", "docker-buildx", "protoc", "go"}
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
		if dest != expectedAp {
			t.Fatalf("expected symlink %s to point to %s, got %s", linkPath, expectedAp, dest)
		}
		if !strings.Contains(output, linkPath) {
			t.Errorf("expected output to mention %s, got: %s", linkPath, output)
		}
	}

	// Test refreshing existing links
	buf.Reset()
	rootCmd.SetArgs([]string{"tool", "install", tempDir})
	if err := rootCmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("unexpected error refreshing ap tool install: %v\nOutput: %s", err, buf.String())
	}

	// Test default dir in repository (${REPO_ROOT}/.build/bin)
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repoDir)

	buf.Reset()
	rootCmd = BuildRootCommand()
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"tool", "install"})
	if err := rootCmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("unexpected error running ap tool install in repository: %v\nOutput: %s", err, buf.String())
	}
	repoBinDir := filepath.Join(repoDir, ".build", "bin")
	repoAp := filepath.Join(repoBinDir, "ap")
	if _, err := os.Stat(repoAp); err != nil {
		t.Fatalf("expected installed ap binary at %s: %v", repoAp, err)
	}
	for _, toolName := range expectedTools {
		linkPath := filepath.Join(repoBinDir, toolName)
		dest, err := os.Readlink(linkPath)
		if err != nil {
			t.Fatalf("failed to readlink in repository bin dir for %s: %v", toolName, err)
		}
		if dest != repoAp {
			t.Fatalf("expected repo symlink %s to point to %s, got %s", linkPath, repoAp, dest)
		}
	}

	// Test default dir outside repository (AP_CACHE_DIR/bin)
	nonRepoDir := t.TempDir()
	t.Chdir(nonRepoDir)
	t.Setenv("REPO_ROOT", "")
	cacheDir := t.TempDir()
	t.Setenv("AP_CACHE_DIR", cacheDir)
	installedDir, err := tool.Install(tool.InstallOptions{
		Stdout: buf,
	})
	if err != nil {
		t.Fatalf("unexpected error running tool.Install with default cache dir: %v", err)
	}
	defaultBinDir := filepath.Join(cacheDir, "bin")
	if installedDir != defaultBinDir {
		t.Fatalf("expected installed dir %s, got %s", defaultBinDir, installedDir)
	}
	for _, toolName := range expectedTools {
		linkPath := filepath.Join(defaultBinDir, toolName)
		if dest, err := os.Readlink(linkPath); err != nil {
			t.Fatalf("failed to readlink in default cache dir for %s: %v", toolName, err)
		} else if dest != filepath.Join(defaultBinDir, "ap") {
			t.Fatalf("expected symlink %s to point to %s, got %s", linkPath, filepath.Join(defaultBinDir, "ap"), dest)
		}
	}
}

func TestToolGo_DryRun(t *testing.T) {
	tempCache := t.TempDir()
	t.Setenv("AP_CACHE_DIR", tempCache)

	tempDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tempDir)

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
