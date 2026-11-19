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

package github

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
)

const (
	// APITimeout is the default timeout for GitHub API operations.
	APITimeout = 5 * time.Minute
)

var (
	retryBackoffs = []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second}
	nowFunc       = time.Now
)

// Release represents a release on GitHub.
type Release struct {
	TagName    string  `json:"tag_name"`
	Name       string  `json:"name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

// Asset represents an asset attached to a GitHub release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
}

// FindAsset finds an asset by exact name.
func FindAsset(assets []Asset, name string) (Asset, bool) {
	for _, a := range assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// RateLimitError represents a rate limit failure from the GitHub API.
type RateLimitError struct {
	msg string
}

func (e *RateLimitError) Error() string {
	return e.msg
}

// IsRateLimitError reports whether err is or wraps a *RateLimitError.
func IsRateLimitError(err error) bool {
	var rle *RateLimitError
	return errors.As(err, &rle)
}

// CloseResponseBody safely closes a response body, ignoring or logging close errors.
func CloseResponseBody(ctx context.Context, resp *http.Response) {
	if resp != nil && resp.Body != nil {
		if err := resp.Body.Close(); err != nil {
			scoped.Log(ctx).Fine("failed to close response body", "error", err)
		}
	}
}

// Token returns the GitHub token from environment variables (GITHUB_TOKEN or GH_TOKEN).
func Token() string {
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t
	}
	return os.Getenv("GH_TOKEN")
}

// NewRequest creates an http.Request with context and standard GitHub headers (Accept, Authorization).
func NewRequest(ctx context.Context, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "gke-labs-ap")

	if token := Token(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
}

type cacheEntry struct {
	URL       string          `json:"url"`
	ETag      string          `json:"etag"`
	FetchedAt string          `json:"fetched_at"`
	Status    int             `json:"status"`
	Body      json.RawMessage `json:"body"`
}

func (e *cacheEntry) bodyBytes() []byte {
	return []byte(e.Body)
}

func cachePath(rawURL string) (string, error) {
	cacheDir, err := fileutils.CacheDir()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(rawURL))
	key := hex.EncodeToString(hash[:])
	return filepath.Join(cacheDir, "github", key+".json"), nil
}

func readCacheEntry(rawURL string) (*cacheEntry, string, error) {
	p, err := cachePath(rawURL)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, p, err
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, p, err
	}
	if entry.ETag == "" || entry.Status != http.StatusOK {
		return nil, p, nil
	}
	return &entry, p, nil
}

func writeCacheEntry(ctx context.Context, p, rawURL, etag string, body []byte) error {
	entry := cacheEntry{
		URL:       rawURL,
		ETag:      etag,
		FetchedAt: nowFunc().UTC().Format(time.RFC3339),
		Status:    http.StatusOK,
		Body:      json.RawMessage(body),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal cache entry: %w", err)
	}
	if err := fileutils.WriteFileAtomic(ctx, p, data, 0644); err != nil {
		return fmt.Errorf("failed to write cache file %s: %w", p, err)
	}
	return nil
}

// Do executes an HTTP request against GitHub, adding caching with ETag revalidation
// and handling transient errors / rate limits.
func Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	isGet := req.Method == http.MethodGet || req.Method == ""
	var p string
	var cachedEntry *cacheEntry

	reqClone := req.Clone(ctx)

	if isGet && req.URL != nil && req.URL.String() != "" {
		entry, cp, err := readCacheEntry(req.URL.String())
		p = cp
		if err != nil {
			scoped.Log(ctx).Fine("cache lookup failed", "url", req.URL.String(), "error", err)
		} else if entry != nil {
			cachedEntry = entry
			if reqClone.Header.Get("If-None-Match") == "" {
				reqClone.Header.Set("If-None-Match", entry.ETag)
			}
		}
	}

	client := scoped.HTTPClient(ctx)
	for attempt := 0; ; attempt++ {
		resp, err := client.Do(reqClone.Clone(ctx))

		rateLimited := err == nil && isRateLimitedResponse(resp)
		transient := err != nil || (resp != nil && resp.StatusCode >= 500)

		if !rateLimited && !transient {
			if err != nil {
				return nil, err
			}
			if isGet && resp.StatusCode == http.StatusNotModified && cachedEntry != nil {
				CloseResponseBody(ctx, resp)
				resp.StatusCode = http.StatusOK
				resp.Status = "200 OK"
				resp.Body = io.NopCloser(bytes.NewReader(cachedEntry.bodyBytes()))
				if resp.Header.Get("Content-Type") == "" {
					resp.Header.Set("Content-Type", "application/json")
				}
				return resp, nil
			}
			if isGet && p != "" && resp.StatusCode == http.StatusOK {
				etag := strings.TrimSpace(resp.Header.Get("ETag"))
				if etag != "" {
					body, readErr := io.ReadAll(resp.Body)
					CloseResponseBody(ctx, resp)
					if readErr != nil {
						return nil, fmt.Errorf("failed to read response body: %w", readErr)
					}
					resp.Body = io.NopCloser(bytes.NewReader(body))

					if err := writeCacheEntry(ctx, p, req.URL.String(), etag, body); err != nil {
						scoped.Log(ctx).Fine("failed to write github cache", "error", err)
					}
				}
			}
			return resp, nil
		}

		if attempt == len(retryBackoffs) {
			if rateLimited {
				CloseResponseBody(ctx, resp)
				return nil, buildRateLimitError(ctx, resp, "")
			}
			return resp, err
		}

		var wait time.Duration
		if rateLimited {
			var resetIn time.Duration
			var resetTime time.Time
			wait, resetIn, resetTime = computeRateLimitWait(ctx, resp)
			if wait == 0 {
				CloseResponseBody(ctx, resp)
				return nil, buildRateLimitError(ctx, resp, "")
			}
			if deadline, hasDeadline := ctx.Deadline(); hasDeadline && time.Now().Add(wait).After(deadline) {
				CloseResponseBody(ctx, resp)
				remain := time.Until(deadline)
				if remain < 0 {
					remain = 0
				}
				remainStr := remain.Round(time.Second).String()
				if remain < time.Second {
					remainStr = remain.Round(time.Millisecond).String()
				}
				reason := fmt.Sprintf("resets in %s, but only %s remain before the deadline", resetIn.Round(time.Second), remainStr)
				return nil, buildRateLimitError(ctx, resp, reason)
			}
			scoped.Log(ctx).Warn("GitHub API rate limited; waiting before retrying", "wait", wait, "reset", resetTime)
		} else {
			wait = retryBackoffs[attempt]
		}

		CloseResponseBody(ctx, resp)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

func isRateLimitedResponse(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return false
	}
	if strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")) == "0" {
		return true
	}
	if strings.TrimSpace(resp.Header.Get("Retry-After")) != "" {
		return true
	}
	return false
}

func computeRateLimitWait(ctx context.Context, resp *http.Response) (wait time.Duration, resetIn time.Duration, resetTime time.Time) {
	now := nowFunc()
	if dateStr := resp.Header.Get("Date"); dateStr != "" {
		serverTime, err := http.ParseTime(dateStr)
		if err != nil {
			scoped.Log(ctx).Fine("failed to parse Date header", "header", dateStr, "error", err)
		} else {
			now = serverTime.UTC()
		}
	}

	if resetStr := resp.Header.Get("X-RateLimit-Reset"); resetStr != "" {
		if resetUnix, err := strconv.ParseInt(strings.TrimSpace(resetStr), 10, 64); err == nil && resetUnix > 0 {
			resetTime = time.Unix(resetUnix, 0).UTC()
		}
	}

	if retryAfterStr := strings.TrimSpace(resp.Header.Get("Retry-After")); retryAfterStr != "" {
		if secs, err := strconv.Atoi(retryAfterStr); err == nil && secs >= 0 {
			wait = time.Duration(secs) * time.Second
			resetIn = wait
			if resetTime.IsZero() {
				resetTime = nowFunc().Add(wait)
			}
			return wait, resetIn, resetTime
		}
	}

	if !resetTime.IsZero() {
		diff := resetTime.Sub(now)
		if diff < 0 {
			diff = 0
		}
		resetIn = diff
		wait = diff + 1*time.Second
		return wait, resetIn, resetTime
	}

	return 0, 0, resetTime
}

func buildRateLimitError(ctx context.Context, resp *http.Response, reason string) error {
	var resetInfo string
	if reason != "" {
		resetInfo = fmt.Sprintf(" (%s)", reason)
	} else if resetStr := resp.Header.Get("X-RateLimit-Reset"); resetStr != "" {
		if resetUnix, err := strconv.ParseInt(strings.TrimSpace(resetStr), 10, 64); err == nil && resetUnix > 0 {
			resetTime := time.Unix(resetUnix, 0).UTC()
			now := nowFunc()
			if dateStr := resp.Header.Get("Date"); dateStr != "" {
				serverTime, err := http.ParseTime(dateStr)
				if err != nil {
					scoped.Log(ctx).Fine("failed to parse Date header", "header", dateStr, "error", err)
				} else {
					now = serverTime.UTC()
				}
			}
			diff := resetTime.Sub(now)
			mins := int(math.Round(diff.Minutes()))
			if mins < 0 {
				mins = 0
			}
			resetInfo = fmt.Sprintf(" (resets at %s, in %dm)", resetTime.Format("15:04 UTC"), mins)
		}
	}
	return &RateLimitError{
		msg: fmt.Sprintf("GitHub API rate limit exceeded%s; pin a version listed in versions.yaml, or set GITHUB_TOKEN for this one command", resetInfo),
	}
}

// FetchRelease fetches release metadata from a GitHub release URL.
func FetchRelease(ctx context.Context, url string) (*Release, error) {
	req, err := NewRequest(ctx, url)
	if err != nil {
		return nil, err
	}
	resp, err := Do(ctx, req)
	if err != nil {
		return nil, err
	}
	defer CloseResponseBody(ctx, resp)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s returned status %s", url, resp.Status)
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("failed to decode GitHub release response: %w", err)
	}
	return &rel, nil
}

// ListReleases fetches releases for ownerRepo (e.g. "protocolbuffers/protobuf" or "docker/buildx")
// from apiBase, following Link headers for pagination until max releases are reached (or all if max <= 0).
func ListReleases(ctx context.Context, apiBase, ownerRepo string, max int) ([]Release, error) {
	var allReleases []Release
	nextURL := fmt.Sprintf("%s/repos/%s/releases?per_page=100", strings.TrimRight(apiBase, "/"), ownerRepo)

	for nextURL != "" && (max <= 0 || len(allReleases) < max) {
		page, next, err := fetchReleasesPage(ctx, nextURL)
		if err != nil {
			return nil, err
		}
		nextURL = next
		allReleases = append(allReleases, page...)
	}

	if max > 0 && len(allReleases) > max {
		allReleases = allReleases[:max]
	}

	return allReleases, nil
}

func fetchReleasesPage(ctx context.Context, url string) ([]Release, string, error) {
	req, err := NewRequest(ctx, url)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create request for %s: %w", url, err)
	}

	resp, err := Do(ctx, req)
	if err != nil {
		return nil, "", err
	}
	defer CloseResponseBody(ctx, resp)

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("GitHub releases API returned status %s for %s", resp.Status, url)
	}

	var pageReleases []Release
	if err := json.NewDecoder(resp.Body).Decode(&pageReleases); err != nil {
		return nil, "", fmt.Errorf("failed to decode GitHub releases: %w", err)
	}

	nextURL := parseNextLink(strings.Join(resp.Header["Link"], ","))
	return pageReleases, nextURL, nil
}

// GetText fetches a URL via Do and returns its body as a string.
func GetText(ctx context.Context, url string) (string, error) {
	req, err := NewRequest(ctx, url)
	if err != nil {
		return "", err
	}
	resp, err := Do(ctx, req)
	if err != nil {
		return "", err
	}
	defer CloseResponseBody(ctx, resp)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s returned HTTP status %s", url, resp.Status)
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// parseNextLink parses the next page URL from a Link header (<url>; rel="next").
// It handles single or comma-separated link values without regex.
func parseNextLink(linkHeader string) string {
	if linkHeader == "" {
		return ""
	}
	for _, part := range strings.Split(linkHeader, ",") {
		part = strings.TrimSpace(part)
		pieces := strings.Split(part, ";")
		if len(pieces) < 2 {
			continue
		}
		rawURL := strings.TrimSpace(pieces[0])
		if !strings.HasPrefix(rawURL, "<") || !strings.HasSuffix(rawURL, ">") {
			continue
		}
		targetURL := rawURL[1 : len(rawURL)-1]
		if targetURL == "" {
			continue
		}

		for _, param := range pieces[1:] {
			param = strings.TrimSpace(param)
			k, v, ok := strings.Cut(param, "=")
			if !ok {
				continue
			}
			if strings.TrimSpace(k) == "rel" {
				relVal := strings.Trim(strings.TrimSpace(v), `"`)
				for _, r := range strings.Fields(relVal) {
					if r == "next" {
						return targetURL
					}
				}
			}
		}
	}
	return ""
}
