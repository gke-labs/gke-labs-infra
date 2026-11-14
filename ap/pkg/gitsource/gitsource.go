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
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
)

// Target represents a resolved Git deployment target.
type Target struct {
	RepoURL string
	Ref     string
	Commit  string
	Path    string
}

type matchResult struct {
	Ref    string
	Commit string
	Path   string
}

// IsURL reports whether target is a supported Git repository URL.
func IsURL(target string) bool {
	trimmed := strings.TrimSpace(target)
	return strings.HasPrefix(trimmed, "https://") ||
		strings.HasPrefix(trimmed, "http://") ||
		strings.HasPrefix(trimmed, "file://")
}

func isCommitSHA(candidate string) bool {
	if len(candidate) != 40 {
		return false
	}
	for _, character := range candidate {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') && (character < 'A' || character > 'F') {
			return false
		}
	}
	return true
}

func listRemoteRefs(ctx context.Context, repoURL string) (map[string]string, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", repoURL)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-remote failed for %s: %w", repoURL, err)
	}

	remoteRefs := make(map[string]string)
	for _, line := range strings.Split(string(output), "\n") {
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" {
			continue
		}
		fields := strings.Fields(trimmedLine)
		if len(fields) < 2 {
			continue
		}
		commitSHA := fields[0]
		refName := fields[1]

		if refName == "HEAD" {
			remoteRefs["HEAD"] = commitSHA
			continue
		}

		if strings.HasPrefix(refName, "refs/heads/") {
			branchName := strings.TrimPrefix(refName, "refs/heads/")
			remoteRefs[branchName] = commitSHA
			continue
		}

		if strings.HasPrefix(refName, "refs/tags/") {
			tagRef := strings.TrimPrefix(refName, "refs/tags/")
			if strings.HasSuffix(tagRef, "^{}") {
				tagName := strings.TrimSuffix(tagRef, "^{}")
				remoteRefs[tagName] = commitSHA
			} else if _, exists := remoteRefs[tagRef]; !exists {
				remoteRefs[tagRef] = commitSHA
			}
		}
	}
	return remoteRefs, nil
}

func matchRefAndPath(ctx context.Context, repoURL, candidate string) (*matchResult, error) {
	candidate = strings.Trim(candidate, "/")

	remoteRefs, err := listRemoteRefs(ctx, repoURL)
	if err != nil {
		return nil, err
	}

	if candidate == "" || candidate == "." {
		headSHA, found := remoteRefs["HEAD"]
		if !found {
			return nil, fmt.Errorf("could not find HEAD ref in %s", repoURL)
		}
		return &matchResult{
			Ref:    "HEAD",
			Commit: headSHA,
			Path:   ".",
		}, nil
	}

	parts := strings.Split(candidate, "/")
	for i := len(parts); i >= 1; i-- {
		refCandidate := strings.Join(parts[:i], "/")
		if commitSHA, found := remoteRefs[refCandidate]; found {
			remainingPath := strings.Join(parts[i:], "/")
			if remainingPath == "" {
				remainingPath = "."
			}
			return &matchResult{
				Ref:    refCandidate,
				Commit: commitSHA,
				Path:   remainingPath,
			}, nil
		}
	}

	if isCommitSHA(parts[0]) {
		remainingPath := strings.Join(parts[1:], "/")
		if remainingPath == "" {
			remainingPath = "."
		}
		return &matchResult{
			Ref:    parts[0],
			Commit: parts[0],
			Path:   remainingPath,
		}, nil
	}

	return nil, fmt.Errorf("could not match ref in %q against branches or tags in %s", candidate, repoURL)
}

func extractCandidate(candidateURL string) string {
	parsedURL, err := url.Parse(candidateURL)
	if err != nil {
		return ""
	}
	trimmedPath := strings.Trim(parsedURL.Path, "/")
	pathSegments := strings.Split(trimmedPath, "/")
	if len(pathSegments) >= 3 {
		remainingSegments := pathSegments[2:]
		if len(remainingSegments) > 0 && remainingSegments[0] == "blob" {
			remainingSegments = remainingSegments[1:]
		}
		return strings.Join(remainingSegments, "/")
	}
	return ""
}

func resolveKustomizeStyle(ctx context.Context, parsedURL *url.URL) (*Target, error) {
	pathParts := strings.SplitN(parsedURL.Path, "//", 2)
	repoPath := pathParts[0]
	subPath := strings.Trim(pathParts[1], "/")
	if subPath == "" {
		subPath = "."
	}

	var repoURL string
	if parsedURL.Scheme == "file" {
		repoURL = "file://" + repoPath
	} else {
		repoURL = fmt.Sprintf("%s://%s%s", parsedURL.Scheme, parsedURL.Host, repoPath)
		if !strings.HasSuffix(repoURL, ".git") {
			repoURL += ".git"
		}
	}

	ref := parsedURL.Query().Get("ref")
	remoteRefs, err := listRemoteRefs(ctx, repoURL)
	if err != nil {
		return nil, err
	}

	var commitSHA string
	if ref == "" {
		headSHA, found := remoteRefs["HEAD"]
		if !found {
			return nil, fmt.Errorf("could not find HEAD ref in %s", repoURL)
		}
		commitSHA = headSHA
	} else if isCommitSHA(ref) {
		commitSHA = ref
	} else if sha, found := remoteRefs[ref]; found {
		commitSHA = sha
	} else {
		return nil, fmt.Errorf("could not find ref %q in %s", ref, repoURL)
	}

	return &Target{
		RepoURL: repoURL,
		Ref:     ref,
		Commit:  commitSHA,
		Path:    subPath,
	}, nil
}

func resolveRawGitHub(ctx context.Context, parsedURL *url.URL) (*Target, error) {
	trimmedPath := strings.Trim(parsedURL.Path, "/")
	pathSegments := strings.Split(trimmedPath, "/")
	if len(pathSegments) < 2 {
		return nil, fmt.Errorf("invalid GitHub raw URL %q", parsedURL.String())
	}
	organization := pathSegments[0]
	repository := strings.TrimSuffix(pathSegments[1], ".git")
	repoURL := fmt.Sprintf("https://github.com/%s/%s.git", organization, repository)

	remainingSegments := pathSegments[2:]
	if len(remainingSegments) > 0 && remainingSegments[0] == "blob" {
		remainingSegments = remainingSegments[1:]
	}
	candidate := strings.Join(remainingSegments, "/")

	match, err := matchRefAndPath(ctx, repoURL, candidate)
	if err != nil {
		return nil, err
	}
	return &Target{
		RepoURL: repoURL,
		Ref:     match.Ref,
		Commit:  match.Commit,
		Path:    match.Path,
	}, nil
}

func resolveFromHeaders(ctx context.Context, parsedURL *url.URL) (*Target, error) {
	cleanURL := parsedURL.String()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, cleanURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request for %s: %w", cleanURL, err)
	}
	request.Header.Set("User-Agent", "ap/1.0")

	response, err := scoped.HTTPClient(ctx).Do(request)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch %s: %w", cleanURL, err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			scoped.Log(ctx).Info("failed to close response body", "error", closeErr)
		}
	}()

	repoDownload := response.Header.Get("x-repository-download")
	if repoDownload == "" {
		return nil, fmt.Errorf("URL %q did not return an x-repository-download header", cleanURL)
	}

	repoURL := strings.TrimPrefix(strings.TrimSpace(repoDownload), "git clone ")
	repoURL = strings.TrimSpace(repoURL)

	var candidate string
	rawDownload := response.Header.Get("x-raw-download")
	if rawDownload != "" {
		candidate = extractCandidate(rawDownload)
	}
	if candidate == "" {
		finalURL := cleanURL
		if response.Request != nil && response.Request.URL != nil {
			finalURL = response.Request.URL.String()
		}
		candidate = extractCandidate(finalURL)
	}
	if candidate == "" {
		return nil, fmt.Errorf("could not determine path from URL %q", cleanURL)
	}

	match, err := matchRefAndPath(ctx, repoURL, candidate)
	if err != nil {
		return nil, err
	}
	return &Target{
		RepoURL: repoURL,
		Ref:     match.Ref,
		Commit:  match.Commit,
		Path:    match.Path,
	}, nil
}

// ResolveTarget parses a Git or web URL into a Target containing RepoURL, Ref, Commit, and Path.
func ResolveTarget(ctx context.Context, rawURL string) (*Target, error) {
	cleanURL := strings.TrimSpace(rawURL)
	parsedURL, err := url.Parse(cleanURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}

	if strings.Contains(parsedURL.Path, "//") {
		return resolveKustomizeStyle(ctx, parsedURL)
	}
	if parsedURL.Host == "raw.github.com" || parsedURL.Host == "raw.githubusercontent.com" {
		return resolveRawGitHub(ctx, parsedURL)
	}
	return resolveFromHeaders(ctx, parsedURL)
}

func repoCacheDir(cacheRoot, repoURL string) (string, error) {
	cleanURL := strings.TrimSpace(repoURL)
	if strings.Contains(cleanURL, "://") {
		parsedURL, err := url.Parse(cleanURL)
		if err != nil {
			return "", fmt.Errorf("invalid repository URL %q: %w", repoURL, err)
		}
		host := parsedURL.Host
		path := strings.TrimSuffix(strings.Trim(parsedURL.Path, "/"), ".git")
		return filepath.Join(cacheRoot, "git", host, filepath.FromSlash(path)), nil
	}

	sanitized := strings.Trim(filepath.Clean(cleanURL), string(filepath.Separator))
	return filepath.Join(cacheRoot, "git", "local", sanitized), nil
}

func updateExisting(ctx context.Context, repoDirectory string, target *Target) error {
	fetchCmd := exec.CommandContext(ctx, "git", "-C", repoDirectory, "fetch", "origin")
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to fetch git repository %s: %w\n%s", target.RepoURL, err, string(output))
	}

	checkoutCmd := exec.CommandContext(ctx, "git", "-C", repoDirectory, "checkout", "--force", "--detach", target.Commit)
	if output, err := checkoutCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to checkout commit %s in %s: %w\n%s", target.Commit, target.RepoURL, err, string(output))
	}

	return nil
}

func cloneFresh(ctx context.Context, repoDirectory string, target *Target) error {
	if err := os.RemoveAll(repoDirectory); err != nil {
		return fmt.Errorf("failed to remove repo dir for %s: %w", target.RepoURL, err)
	}
	if err := os.MkdirAll(filepath.Dir(repoDirectory), 0755); err != nil {
		return fmt.Errorf("failed to create repo parent dir for %s: %w", target.RepoURL, err)
	}
	cloneCmd := exec.CommandContext(ctx, "git", "clone", "--quiet", target.RepoURL, repoDirectory)
	if output, err := cloneCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to git clone %s: %w\n%s", target.RepoURL, err, string(output))
	}

	checkoutCmd := exec.CommandContext(ctx, "git", "-C", repoDirectory, "checkout", "--force", "--detach", target.Commit)
	if output, err := checkoutCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to checkout commit %s in %s: %w\n%s", target.Commit, target.RepoURL, err, string(output))
	}

	return nil
}

// CloneOrUpdate clones or fetches target.RepoURL into a subdirectory of cacheRoot (derived via fileutils.CacheDir),
// checks out target.Commit in detached HEAD mode, and returns the path to the repository on disk.
func CloneOrUpdate(ctx context.Context, target *Target) (string, error) {
	cacheRoot, err := fileutils.CacheDir()
	if err != nil {
		return "", fmt.Errorf("failed to determine cache dir: %w", err)
	}

	repoDirectory, err := repoCacheDir(cacheRoot, target.RepoURL)
	if err != nil {
		return "", err
	}

	gitDirectory := filepath.Join(repoDirectory, ".git")
	if fileInfo, err := os.Stat(gitDirectory); err == nil && fileInfo.IsDir() {
		if err := updateExisting(ctx, repoDirectory, target); err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return "", err
			}
			scoped.Log(ctx).Info("cached repository update failed; recloning", "directory", repoDirectory, "error", err)
			if err := cloneFresh(ctx, repoDirectory, target); err != nil {
				return "", err
			}
		}
		return repoDirectory, nil
	}

	if err := cloneFresh(ctx, repoDirectory, target); err != nil {
		return "", err
	}

	return repoDirectory, nil
}
