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

package gitsource

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
)

func createTestRepo(t *testing.T) (string, string, string) {
	t.Helper()
	repoDir := t.TempDir()

	runGit := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=Test User",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test User",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s failed: %v\nOutput: %s", strings.Join(args, " "), err, string(output))
		}
		return strings.TrimSpace(string(output))
	}

	runGit("init")
	runGit("checkout", "-b", "main")

	filePath := filepath.Join(repoDir, "file.txt")
	if err := os.WriteFile(filePath, []byte("main-content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "file.txt")
	runGit("commit", "-m", "initial commit")
	mainCommit := runGit("rev-parse", "HEAD")

	// Tag "release" (shorter ref)
	runGit("tag", "release")

	// Branch "release/v1" (longer ref)
	runGit("checkout", "-b", "release/v1")
	if err := os.WriteFile(filePath, []byte("release-v1-content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "file.txt")
	runGit("commit", "-m", "release v1 commit")
	releaseV1Commit := runGit("rev-parse", "HEAD")

	runGit("checkout", "main")

	return repoDir, mainCommit, releaseV1Commit
}

func TestResolveTarget_Table(t *testing.T) {
	repoDir, mainCommit, releaseV1Commit := createTestRepo(t)

	// Configure git to route https://github.com/testorg/testrepo.git to repoDir
	configFile := filepath.Join(t.TempDir(), "gitconfig")
	configContent := fmt.Sprintf("[url %q]\n\tinsteadOf = https://github.com/testorg/testrepo.git\n", repoDir)
	if err := os.WriteFile(configFile, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", configFile)

	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if strings.Contains(request.URL.Path, "no-headers") {
			responseWriter.WriteHeader(http.StatusOK)
			return
		}
		responseWriter.Header().Set("x-repository-download", "git clone "+repoDir)
		responseWriter.Header().Set("x-raw-download", "https://raw.githubusercontent.com/testorg/testrepo/main/cert-manager/kustomization.yaml")
		responseWriter.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx := scoped.WithHTTPClient(t.Context(), server.Client())

	tests := []struct {
		name        string
		rawURL      string
		wantRepoURL string
		wantRef     string
		wantCommit  string
		wantPath    string
		wantErr     bool
	}{
		{
			name:        "kustomize-style",
			rawURL:      "file://" + repoDir + "//cert-manager/kustomization.yaml?ref=main",
			wantRepoURL: "file://" + repoDir,
			wantRef:     "main",
			wantCommit:  mainCommit,
			wantPath:    "cert-manager/kustomization.yaml",
		},
		{
			name:        "raw well-known",
			rawURL:      "https://raw.github.com/testorg/testrepo/blob/main/cert-manager/kustomization.yaml",
			wantRepoURL: "https://github.com/testorg/testrepo.git",
			wantRef:     "main",
			wantCommit:  mainCommit,
			wantPath:    "cert-manager/kustomization.yaml",
		},
		{
			name:        "web link with headers",
			rawURL:      server.URL + "/testorg/testrepo/blob/main/cert-manager/kustomization.yaml",
			wantRepoURL: repoDir,
			wantRef:     "main",
			wantCommit:  mainCommit,
			wantPath:    "cert-manager/kustomization.yaml",
		},
		{
			name:    "web link without headers errors",
			rawURL:  server.URL + "/no-headers/blob/main/cert-manager/kustomization.yaml",
			wantErr: true,
		},
		{
			name:        "ambiguous release/v1/path picks longest ref",
			rawURL:      "https://raw.github.com/testorg/testrepo/blob/release/v1/cert-manager/kustomization.yaml",
			wantRepoURL: "https://github.com/testorg/testrepo.git",
			wantRef:     "release/v1",
			wantCommit:  releaseV1Commit,
			wantPath:    "cert-manager/kustomization.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := ResolveTarget(ctx, tt.rawURL)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ResolveTarget(%q) expected error, got nil", tt.rawURL)
				}
				if !strings.Contains(err.Error(), "x-repository-download") {
					t.Errorf("expected error mentioning x-repository-download, got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveTarget(%q) unexpected error: %v", tt.rawURL, err)
			}
			if target.RepoURL != tt.wantRepoURL {
				t.Errorf("target.RepoURL = %q, want %q", target.RepoURL, tt.wantRepoURL)
			}
			if target.Ref != tt.wantRef {
				t.Errorf("target.Ref = %q, want %q", target.Ref, tt.wantRef)
			}
			if target.Commit != tt.wantCommit {
				t.Errorf("target.Commit = %q, want %q", target.Commit, tt.wantCommit)
			}
			if target.Path != tt.wantPath {
				t.Errorf("target.Path = %q, want %q", target.Path, tt.wantPath)
			}
		})
	}
}

func TestCloneOrUpdate(t *testing.T) {
	repoDir, mainCommit, _ := createTestRepo(t)
	cacheDir := t.TempDir()
	t.Setenv("AP_CACHE_DIR", cacheDir)
	ctx := t.Context()

	targetMain := &Target{
		RepoURL: repoDir,
		Ref:     "main",
		Commit:  mainCommit,
		Path:    "file.txt",
	}

	cloneDir, err := CloneOrUpdate(ctx, targetMain)
	if err != nil {
		t.Fatalf("CloneOrUpdate initial clone failed: %v", err)
	}

	clonedFile := filepath.Join(cloneDir, "file.txt")
	content, err := os.ReadFile(clonedFile)
	if err != nil {
		t.Fatalf("failed to read cloned file: %v", err)
	}
	if string(content) != "main-content\n" {
		t.Errorf("cloned file content = %q, want %q", string(content), "main-content\n")
	}

	// Make a second commit on main in repoDir
	runGit := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=Test User",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test User",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s failed: %v\nOutput: %s", strings.Join(args, " "), err, string(output))
		}
		return strings.TrimSpace(string(output))
	}
	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("updated-content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "file.txt")
	runGit("commit", "-m", "second commit")
	secondCommit := runGit("rev-parse", "HEAD")

	targetUpdated := &Target{
		RepoURL: repoDir,
		Ref:     "main",
		Commit:  secondCommit,
		Path:    "file.txt",
	}

	updatedCloneDir, err := CloneOrUpdate(ctx, targetUpdated)
	if err != nil {
		t.Fatalf("CloneOrUpdate update failed: %v", err)
	}
	if updatedCloneDir != cloneDir {
		t.Errorf("expected same cloneDir %q, got %q", cloneDir, updatedCloneDir)
	}

	updatedContent, err := os.ReadFile(clonedFile)
	if err != nil {
		t.Fatalf("failed to read updated cloned file: %v", err)
	}
	if string(updatedContent) != "updated-content\n" {
		t.Errorf("updated cloned file content = %q, want %q", string(updatedContent), "updated-content\n")
	}
}
