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
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"k8s.io/klog/v2"
)

var (
	execCommandContext = exec.CommandContext
	httpClient         = &http.Client{Timeout: 15 * time.Second}
)

// Target represents a resolved Git deployment target.
type Target struct {
	RepoURL string
	Ref     string
	Path    string
}

// IsURL reports whether target is a Git/HTTP URL.
func IsURL(target string) bool {
	s := strings.TrimSpace(target)
	return strings.HasPrefix(s, "http://") ||
		strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "git::")
}

// isCommitSHA reports whether s is a 40-character or 64-character hexadecimal commit hash.
func isCommitSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// ListRemoteRefs queries remote branches and tags using "git ls-remote --heads --tags <repoURL>".
func ListRemoteRefs(ctx context.Context, repoURL string) (branches []string, tags []string, err error) {
	cmd := execCommandContext(ctx, "git", "ls-remote", "--heads", "--tags", repoURL)
	out, err := cmd.Output()
	if err != nil {
		return nil, nil, fmt.Errorf("git ls-remote failed for %s: %w", repoURL, err)
	}

	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		ref := fields[1]
		if strings.HasPrefix(ref, "refs/heads/") {
			branches = append(branches, strings.TrimPrefix(ref, "refs/heads/"))
		} else if strings.HasPrefix(ref, "refs/tags/") {
			tag := strings.TrimPrefix(ref, "refs/tags/")
			tag = strings.TrimSuffix(tag, "^{}")
			tags = append(tags, tag)
		}
	}
	return branches, tags, nil
}

// MatchRefAndPath greedily matches a candidate string against the remote repository's branches and tags.
// It returns the matched ref and the remaining subpath.
func MatchRefAndPath(ctx context.Context, repoURL, candidate string) (string, string, error) {
	candidate = strings.Trim(candidate, "/")
	if candidate == "" || candidate == "." {
		return "", ".", nil
	}

	branches, tags, err := ListRemoteRefs(ctx, repoURL)
	if err != nil {
		return "", "", err
	}

	refSet := make(map[string]bool, len(branches)+len(tags)+1)
	for _, b := range branches {
		refSet[b] = true
	}
	for _, t := range tags {
		refSet[t] = true
	}
	refSet["HEAD"] = true

	parts := strings.Split(candidate, "/")
	for i := len(parts); i >= 1; i-- {
		refCandidate := strings.Join(parts[:i], "/")
		if refSet[refCandidate] {
			rem := strings.Join(parts[i:], "/")
			if rem == "" {
				rem = "."
			}
			return refCandidate, rem, nil
		}
	}

	// Check if parts[0] is a commit SHA
	if isCommitSHA(parts[0]) {
		rem := strings.Join(parts[1:], "/")
		if rem == "" {
			rem = "."
		}
		return parts[0], rem, nil
	}

	return "", "", fmt.Errorf("could not match ref in %q against branches or tags in %s", candidate, repoURL)
}

func fetchHeaders(ctx context.Context, rawURL string) (http.Header, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "ap/1.0")

	resp, err := httpClient.Do(req)
	if err == nil {
		if resp.StatusCode == http.StatusMethodNotAllowed || (resp.StatusCode >= 400 && resp.StatusCode != http.StatusNotFound) {
			if closeErr := resp.Body.Close(); closeErr != nil {
				klog.V(4).Infof("error closing head response body: %v", closeErr)
			}
			getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
			if err == nil {
				getReq.Header.Set("User-Agent", "ap/1.0")
				resp, err = httpClient.Do(getReq)
			}
		}
	}
	if err != nil {
		return nil, "", err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			klog.V(4).Infof("error closing response body: %v", closeErr)
		}
	}()

	finalURL := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	return resp.Header, finalURL, nil
}

func extractCandidateFromRawURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	trimmed := strings.Trim(u.Path, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) >= 3 {
		rest := parts[2:]
		if len(rest) > 0 && rest[0] == "blob" {
			rest = rest[1:]
		}
		return strings.Join(rest, "/")
	}
	return ""
}

func extractCandidateFromWebURL(webURL string) string {
	u, err := url.Parse(webURL)
	if err != nil {
		return ""
	}
	trimmed := strings.Trim(u.Path, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) >= 3 {
		rest := parts[2:]
		if len(rest) > 0 && (rest[0] == "blob" || rest[0] == "tree") {
			rest = rest[1:]
		}
		return strings.Join(rest, "/")
	}
	return ""
}

// ResolveTarget parses a Git or web URL into a Target containing RepoURL, Ref, and Path.
func ResolveTarget(ctx context.Context, rawURL string) (*Target, error) {
	cleanURL := strings.TrimSpace(rawURL)
	cleanURL = strings.TrimPrefix(cleanURL, "git::")

	u, err := url.Parse(cleanURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}

	// 1. Kustomize-style syntax: double slash in path (e.g. //cert-manager/kustomization.yaml?ref=main)
	if strings.Contains(u.Path, "//") {
		parts := strings.SplitN(u.Path, "//", 2)
		repoPath := parts[0]
		subPath := parts[1]
		ref := u.Query().Get("ref")

		var repoURL string
		if u.Scheme != "" {
			repoURL = fmt.Sprintf("%s://%s%s", u.Scheme, u.Host, repoPath)
			if !strings.HasSuffix(repoURL, ".git") {
				repoURL += ".git"
			}
		} else {
			repoURL = repoPath
		}
		subPath = strings.Trim(subPath, "/")
		if subPath == "" {
			subPath = "."
		}
		return &Target{
			RepoURL: repoURL,
			Ref:     ref,
			Path:    subPath,
		}, nil
	}

	// 2. Fetch URL headers to discover x-repository-download / x-raw-download
	header, finalURL, fetchErr := fetchHeaders(ctx, cleanURL)
	if fetchErr != nil {
		klog.V(4).Infof("failed to fetch headers for %s: %v", cleanURL, fetchErr)
	} else {
		repoDownload := header.Get("x-repository-download")
		if repoDownload != "" {
			repoURL := strings.TrimPrefix(strings.TrimSpace(repoDownload), "git clone ")
			repoURL = strings.TrimSpace(repoURL)

			candidate := ""
			rawDownload := header.Get("x-raw-download")
			if rawDownload != "" {
				candidate = extractCandidateFromRawURL(rawDownload)
			}
			if candidate == "" {
				candidate = extractCandidateFromWebURL(finalURL)
			}
			if candidate != "" {
				ref, path, err := MatchRefAndPath(ctx, repoURL, candidate)
				if err != nil {
					return nil, err
				}
				return &Target{
					RepoURL: repoURL,
					Ref:     ref,
					Path:    path,
				}, nil
			}
		}
	}

	// 3. Fallback: Well-known URL formats
	// GitHub raw links: https://raw.github.com/<org>/<repo>/blob/<branch>/<path>
	// or https://raw.githubusercontent.com/<org>/<repo>/<branch>/<path>
	if u.Host == "raw.github.com" || u.Host == "raw.githubusercontent.com" {
		trimmedPath := strings.Trim(u.Path, "/")
		parts := strings.Split(trimmedPath, "/")
		if len(parts) >= 2 {
			org := parts[0]
			repo := strings.TrimSuffix(parts[1], ".git")
			repoURL := fmt.Sprintf("https://github.com/%s/%s.git", org, repo)

			rest := parts[2:]
			if len(rest) > 0 && rest[0] == "blob" {
				rest = rest[1:]
			}
			candidate := strings.Join(rest, "/")
			if candidate == "" {
				candidate = "."
			}
			ref, path, err := MatchRefAndPath(ctx, repoURL, candidate)
			if err != nil {
				return nil, err
			}
			return &Target{
				RepoURL: repoURL,
				Ref:     ref,
				Path:    path,
			}, nil
		}
	}

	// GitHub web links: https://github.com/<org>/<repo>/blob/<branch>/<path>
	if u.Host == "github.com" {
		trimmedPath := strings.Trim(u.Path, "/")
		parts := strings.Split(trimmedPath, "/")
		if len(parts) >= 4 && (parts[2] == "blob" || parts[2] == "tree") {
			org := parts[0]
			repo := strings.TrimSuffix(parts[1], ".git")
			repoURL := fmt.Sprintf("https://github.com/%s/%s.git", org, repo)
			candidate := strings.Join(parts[3:], "/")
			ref, path, err := MatchRefAndPath(ctx, repoURL, candidate)
			if err != nil {
				return nil, err
			}
			return &Target{
				RepoURL: repoURL,
				Ref:     ref,
				Path:    path,
			}, nil
		}
	}

	return nil, fmt.Errorf("unrecognized or unsupported URL format %q", rawURL)
}

// RepoCacheDir returns the local cache directory for a repository URL under cacheRoot.
func RepoCacheDir(cacheRoot, repoURL string) (string, error) {
	cleanURL := strings.TrimSpace(repoURL)
	cleanURL = strings.TrimPrefix(cleanURL, "git::")

	if strings.HasPrefix(cleanURL, "file://") {
		filePath := strings.TrimPrefix(cleanURL, "file://")
		sanitized := strings.Trim(filepath.Clean(filePath), string(filepath.Separator))
		return filepath.Join(cacheRoot, "git", "local", sanitized), nil
	}

	if strings.Contains(cleanURL, "://") {
		u, err := url.Parse(cleanURL)
		if err != nil {
			return "", fmt.Errorf("invalid repository URL %q: %w", repoURL, err)
		}
		host := u.Host
		path := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
		return filepath.Join(cacheRoot, "git", host, filepath.FromSlash(path)), nil
	}

	if atIdx := strings.Index(cleanURL, "@"); atIdx != -1 {
		rest := cleanURL[atIdx+1:]
		if colonIdx := strings.Index(rest, ":"); colonIdx != -1 {
			host := rest[:colonIdx]
			path := strings.TrimSuffix(strings.Trim(rest[colonIdx+1:], "/"), ".git")
			return filepath.Join(cacheRoot, "git", host, filepath.FromSlash(path)), nil
		}
	}

	sanitized := strings.Trim(filepath.Clean(cleanURL), string(filepath.Separator))
	return filepath.Join(cacheRoot, "git", "local", sanitized), nil
}

// CloneOrUpdate clones or fetches target.RepoURL into a subdirectory of cacheRoot (derived via fileutils.CacheDir),
// checks out target.Ref, and returns the path to the repository on disk.
func CloneOrUpdate(ctx context.Context, target *Target) (string, error) {
	cacheRoot, err := fileutils.CacheDir()
	if err != nil {
		return "", fmt.Errorf("failed to determine cache dir: %w", err)
	}

	repoDir, err := RepoCacheDir(cacheRoot, target.RepoURL)
	if err != nil {
		return "", err
	}

	gitDir := filepath.Join(repoDir, ".git")
	if fi, err := os.Stat(gitDir); err == nil && fi.IsDir() {
		cmd := execCommandContext(ctx, "git", "-C", repoDir, "fetch", "--tags", "origin")
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("failed to fetch git repository %s: %w\n%s", target.RepoURL, err, string(out))
		}
		if target.Ref != "" {
			cmd := execCommandContext(ctx, "git", "-C", repoDir, "checkout", "--force", target.Ref)
			if out, err := cmd.CombinedOutput(); err != nil {
				return "", fmt.Errorf("failed to checkout ref %s in %s: %w\n%s", target.Ref, target.RepoURL, err, string(out))
			}
			resetCmd := execCommandContext(ctx, "git", "-C", repoDir, "reset", "--hard", "origin/"+target.Ref)
			if out, err := resetCmd.CombinedOutput(); err != nil {
				klog.V(4).Infof("git reset --hard failed (ref %s may not be a branch): %v\n%s", target.Ref, err, string(out))
			}
		} else {
			pullCmd := execCommandContext(ctx, "git", "-C", repoDir, "pull", "--ff-only")
			if out, err := pullCmd.CombinedOutput(); err != nil {
				klog.V(4).Infof("git pull --ff-only failed: %v\n%s", err, string(out))
			}
		}
		return repoDir, nil
	}

	if err := os.RemoveAll(repoDir); err != nil {
		return "", fmt.Errorf("failed to remove incomplete repo dir: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(repoDir), 0755); err != nil {
		return "", fmt.Errorf("failed to create repo parent dir: %w", err)
	}

	cloneCmd := execCommandContext(ctx, "git", "clone", "--quiet", target.RepoURL, repoDir)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to git clone %s: %w\n%s", target.RepoURL, err, string(out))
	}

	if target.Ref != "" {
		checkoutCmd := execCommandContext(ctx, "git", "-C", repoDir, "checkout", "--force", target.Ref)
		if out, err := checkoutCmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("failed to checkout ref %s in %s: %w\n%s", target.Ref, target.RepoURL, err, string(out))
		}
	}

	return repoDir, nil
}
