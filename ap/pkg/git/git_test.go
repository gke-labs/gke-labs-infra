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

package git

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func createTestRepo(t *testing.T) (string, string) {
	t.Helper()
	repoDir := t.TempDir()

	runGit := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s failed: %v\nOutput: %s", strings.Join(args, " "), err, string(out))
		}
		return strings.TrimSpace(string(out))
	}

	runGit("init")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test User")
	runGit("checkout", "-b", "main")

	// Commit 1 on main
	filePath := filepath.Join(repoDir, "file.txt")
	if err := os.WriteFile(filePath, []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "file.txt")
	runGit("commit", "-m", "initial commit")
	mainCommit := runGit("rev-parse", "HEAD")

	// Tag alpha/nginx/v0.7
	runGit("tag", "alpha/nginx/v0.7")

	// Branch feature/nested
	runGit("checkout", "-b", "feature/nested")
	nestedPath := filepath.Join(repoDir, "nested.txt")
	if err := os.WriteFile(nestedPath, []byte("nested\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "nested.txt")
	runGit("commit", "-m", "nested branch commit")

	// Branch release/v1.0 and release/v1.0-alpha
	runGit("checkout", "-b", "release/v1.0")
	runGit("checkout", "-b", "release/v1.0-alpha")

	runGit("checkout", "main")

	return repoDir, mainCommit
}

func TestIsURL(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"https://github.com/justinsb/packages/blob/main/cert-manager/kustomization.yaml", true},
		{"http://github.com/justinsb/packages/blob/main/cert-manager/kustomization.yaml", true},
		{"git::https://github.com/justinsb/packages//cert-manager/kustomization.yaml?ref=main", true},
		{"cert-manager/kustomization.yaml", false},
		{"./k8s", false},
		{"/var/tmp/k8s", false},
	}

	for _, tt := range tests {
		if got := IsURL(tt.input); got != tt.want {
			t.Errorf("IsURL(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestRepoCacheDir(t *testing.T) {
	cacheRoot := filepath.Join(string(filepath.Separator), "cache", "ap")

	tests := []struct {
		repoURL string
		want    string
	}{
		{
			repoURL: "https://github.com/justinsb/packages.git",
			want:    filepath.Join(cacheRoot, "git", "github.com", "justinsb", "packages"),
		},
		{
			repoURL: "https://github.com/justinsb/packages",
			want:    filepath.Join(cacheRoot, "git", "github.com", "justinsb", "packages"),
		},
		{
			repoURL: "git@github.com:justinsb/packages.git",
			want:    filepath.Join(cacheRoot, "git", "github.com", "justinsb", "packages"),
		},
	}

	for _, tt := range tests {
		got, err := RepoCacheDir(cacheRoot, tt.repoURL)
		if err != nil {
			t.Fatalf("RepoCacheDir(%q) failed: %v", tt.repoURL, err)
		}
		if got != tt.want {
			t.Errorf("RepoCacheDir(%q) = %q, want %q", tt.repoURL, got, tt.want)
		}
	}
}

func TestMatchRefAndPath(t *testing.T) {
	repoDir, mainCommit := createTestRepo(t)
	ctx := t.Context()

	tests := []struct {
		candidate string
		wantRef   string
		wantPath  string
		wantErr   bool
	}{
		{
			candidate: "main/cert-manager/kustomization.yaml",
			wantRef:   "main",
			wantPath:  "cert-manager/kustomization.yaml",
		},
		{
			candidate: "feature/nested/k8s/deployment.yaml",
			wantRef:   "feature/nested",
			wantPath:  "k8s/deployment.yaml",
		},
		{
			candidate: "release/v1.0/cert-manager/kustomization.yaml",
			wantRef:   "release/v1.0",
			wantPath:  "cert-manager/kustomization.yaml",
		},
		{
			candidate: "alpha/nginx/v0.7/cert-manager/kustomization.yaml",
			wantRef:   "alpha/nginx/v0.7",
			wantPath:  "cert-manager/kustomization.yaml",
		},
		{
			candidate: "alpha/nginx/v0.7",
			wantRef:   "alpha/nginx/v0.7",
			wantPath:  ".",
		},
		{
			candidate: mainCommit + "/cert-manager/kustomization.yaml",
			wantRef:   mainCommit,
			wantPath:  "cert-manager/kustomization.yaml",
		},
		{
			candidate: "nonexistent/branch/file.yaml",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		ref, path, err := MatchRefAndPath(ctx, repoDir, tt.candidate)
		if tt.wantErr {
			if err == nil {
				t.Errorf("MatchRefAndPath(%q) expected error, got nil", tt.candidate)
			}
			continue
		}
		if err != nil {
			t.Fatalf("MatchRefAndPath(%q) unexpected error: %v", tt.candidate, err)
		}
		if ref != tt.wantRef {
			t.Errorf("MatchRefAndPath(%q) ref = %q, want %q", tt.candidate, ref, tt.wantRef)
		}
		if path != tt.wantPath {
			t.Errorf("MatchRefAndPath(%q) path = %q, want %q", tt.candidate, path, tt.wantPath)
		}
	}
}

func TestResolveTarget_KustomizeSyntax(t *testing.T) {
	ctx := t.Context()

	t.Run("with ref", func(t *testing.T) {
		urlStr := "https://github.com/justinsb/packages//cert-manager/kustomization.yaml?ref=main"
		target, err := ResolveTarget(ctx, urlStr)
		if err != nil {
			t.Fatalf("ResolveTarget failed: %v", err)
		}

		if target.RepoURL != "https://github.com/justinsb/packages.git" {
			t.Errorf("target.RepoURL = %q, want %q", target.RepoURL, "https://github.com/justinsb/packages.git")
		}
		if target.Ref != "main" {
			t.Errorf("target.Ref = %q, want %q", target.Ref, "main")
		}
		if target.Path != "cert-manager/kustomization.yaml" {
			t.Errorf("target.Path = %q, want %q", target.Path, "cert-manager/kustomization.yaml")
		}
	})

	t.Run("with git:: prefix", func(t *testing.T) {
		urlStr := "git::https://github.com/justinsb/packages//cert-manager/kustomization.yaml?ref=main"
		target, err := ResolveTarget(ctx, urlStr)
		if err != nil {
			t.Fatalf("ResolveTarget failed: %v", err)
		}

		if target.RepoURL != "https://github.com/justinsb/packages.git" {
			t.Errorf("target.RepoURL = %q, want %q", target.RepoURL, "https://github.com/justinsb/packages.git")
		}
		if target.Ref != "main" {
			t.Errorf("target.Ref = %q, want %q", target.Ref, "main")
		}
		if target.Path != "cert-manager/kustomization.yaml" {
			t.Errorf("target.Path = %q, want %q", target.Path, "cert-manager/kustomization.yaml")
		}
	})

	t.Run("without ref", func(t *testing.T) {
		urlStr := "https://github.com/justinsb/packages//cert-manager/kustomization.yaml"
		target, err := ResolveTarget(ctx, urlStr)
		if err != nil {
			t.Fatalf("ResolveTarget failed: %v", err)
		}

		if target.RepoURL != "https://github.com/justinsb/packages.git" {
			t.Errorf("target.RepoURL = %q, want %q", target.RepoURL, "https://github.com/justinsb/packages.git")
		}
		if target.Ref != "" {
			t.Errorf("target.Ref = %q, want empty", target.Ref)
		}
		if target.Path != "cert-manager/kustomization.yaml" {
			t.Errorf("target.Path = %q, want %q", target.Path, "cert-manager/kustomization.yaml")
		}
	})
}

func TestResolveTarget_WithHeaders(t *testing.T) {
	repoDir, _ := createTestRepo(t)
	ctx := t.Context()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-repository-download", "git clone "+repoDir)
		w.Header().Set("x-raw-download", "https://raw.githubusercontent.com/testorg/testrepo/main/cert-manager/kustomization.yaml")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	origClient := httpClient
	httpClient = server.Client()
	defer func() {
		httpClient = origClient
	}()

	target, err := ResolveTarget(ctx, server.URL+"/testorg/testrepo/blob/main/cert-manager/kustomization.yaml")
	if err != nil {
		t.Fatalf("ResolveTarget failed: %v", err)
	}

	if target.RepoURL != repoDir {
		t.Errorf("target.RepoURL = %q, want %q", target.RepoURL, repoDir)
	}
	if target.Ref != "main" {
		t.Errorf("target.Ref = %q, want %q", target.Ref, "main")
	}
	if target.Path != "cert-manager/kustomization.yaml" {
		t.Errorf("target.Path = %q, want %q", target.Path, "cert-manager/kustomization.yaml")
	}
}

func TestResolveTarget_WellKnownRaw(t *testing.T) {
	repoDir, _ := createTestRepo(t)
	ctx := t.Context()

	// Mock git ls-remote to route github.com/testorg/testrepo.git to repoDir
	origExec := execCommandContext
	defer func() {
		execCommandContext = origExec
	}()
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "git" && len(args) >= 4 && args[0] == "ls-remote" {
			args[len(args)-1] = repoDir
		}
		return origExec(ctx, name, args...)
	}

	target, err := ResolveTarget(ctx, "https://raw.github.com/testorg/testrepo/blob/alpha/nginx/v0.7/cert-manager/kustomization.yaml")
	if err != nil {
		t.Fatalf("ResolveTarget failed: %v", err)
	}

	if target.RepoURL != "https://github.com/testorg/testrepo.git" {
		t.Errorf("target.RepoURL = %q, want %q", target.RepoURL, "https://github.com/testorg/testrepo.git")
	}
	if target.Ref != "alpha/nginx/v0.7" {
		t.Errorf("target.Ref = %q, want %q", target.Ref, "alpha/nginx/v0.7")
	}
	if target.Path != "cert-manager/kustomization.yaml" {
		t.Errorf("target.Path = %q, want %q", target.Path, "cert-manager/kustomization.yaml")
	}
}

func TestResolveTarget_WebFallback(t *testing.T) {
	repoDir, _ := createTestRepo(t)
	ctx := t.Context()

	origExec := execCommandContext
	defer func() {
		execCommandContext = origExec
	}()
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "git" && len(args) >= 4 && args[0] == "ls-remote" {
			args[len(args)-1] = repoDir
		}
		return origExec(ctx, name, args...)
	}

	target, err := ResolveTarget(ctx, "https://github.com/testorg/testrepo/blob/main/cert-manager/kustomization.yaml")
	if err != nil {
		t.Fatalf("ResolveTarget failed: %v", err)
	}

	if target.RepoURL != "https://github.com/testorg/testrepo.git" {
		t.Errorf("target.RepoURL = %q, want %q", target.RepoURL, "https://github.com/testorg/testrepo.git")
	}
	if target.Ref != "main" {
		t.Errorf("target.Ref = %q, want %q", target.Ref, "main")
	}
	if target.Path != "cert-manager/kustomization.yaml" {
		t.Errorf("target.Path = %q, want %q", target.Path, "cert-manager/kustomization.yaml")
	}
}

func TestCloneOrUpdate(t *testing.T) {
	repoDir, _ := createTestRepo(t)
	ctx := t.Context()

	cacheDir := t.TempDir()
	t.Setenv("AP_CACHE_DIR", cacheDir)

	target := &Target{
		RepoURL: repoDir,
		Ref:     "feature/nested",
		Path:    "nested.txt",
	}

	cloneDir, err := CloneOrUpdate(ctx, target)
	if err != nil {
		t.Fatalf("CloneOrUpdate failed: %v", err)
	}

	if !strings.HasPrefix(cloneDir, cacheDir) {
		t.Errorf("cloneDir %q is not under cacheDir %q", cloneDir, cacheDir)
	}

	nestedFile := filepath.Join(cloneDir, "nested.txt")
	if _, err := os.Stat(nestedFile); err != nil {
		t.Errorf("expected nested.txt to exist in clone: %v", err)
	}

	// Test update existing clone: switch to main branch
	targetMain := &Target{
		RepoURL: repoDir,
		Ref:     "main",
		Path:    "file.txt",
	}
	cloneDir2, err := CloneOrUpdate(ctx, targetMain)
	if err != nil {
		t.Fatalf("CloneOrUpdate second time failed: %v", err)
	}
	if cloneDir2 != cloneDir {
		t.Errorf("expected same cloneDir %q, got %q", cloneDir, cloneDir2)
	}
	fileTxt := filepath.Join(cloneDir2, "file.txt")
	if _, err := os.Stat(fileTxt); err != nil {
		t.Errorf("expected file.txt to exist in clone on main: %v", err)
	}
}
