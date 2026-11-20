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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
)

func TestDoGitHubRequest(t *testing.T) {
	fixedNow := time.Date(2026, 10, 23, 13, 51, 0, 0, time.UTC)
	oldNow := nowFunc
	nowFunc = func() time.Time { return fixedNow }
	defer func() { nowFunc = oldNow }()

	tests := []struct {
		name            string
		handler         func(reqCount *int) http.HandlerFunc
		clientNow       time.Time
		timeout         time.Duration
		wantStatus      int
		wantErrMsg      string
		wantErrContains string
		wantRequests    int
	}{
		{
			name: "503 then 200 succeeds on the second attempt",
			handler: func(reqCount *int) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					*reqCount++
					if *reqCount == 1 {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					w.WriteHeader(http.StatusOK)
				}
			},
			wantStatus:   http.StatusOK,
			wantRequests: 2,
		},
		{
			name: "403 with Retry-After 1 then 200 succeeds on the second attempt after wait",
			handler: func(reqCount *int) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					*reqCount++
					if *reqCount == 1 {
						w.Header().Set("Retry-After", "1")
						w.WriteHeader(http.StatusForbidden)
						return
					}
					w.WriteHeader(http.StatusOK)
				}
			},
			wantStatus:   http.StatusOK,
			wantRequests: 2,
		},
		{
			name: "403 with Retry-After 1 and 500ms context deadline fails immediately mentioning deadline",
			handler: func(reqCount *int) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					*reqCount++
					if *reqCount == 1 {
						w.Header().Set("Retry-After", "1")
						w.WriteHeader(http.StatusForbidden)
						return
					}
					w.WriteHeader(http.StatusOK)
				}
			},
			timeout:         500 * time.Millisecond,
			wantErrContains: "resets in 1s, but only ",
			wantRequests:    1,
		},
		{
			name: "403 with X-RateLimit-Remaining 0 and X-RateLimit-Reset under default timeout fails fast mentioning deadline",
			handler: func(reqCount *int) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					*reqCount++
					resetTime := fixedNow.Add(41 * time.Minute)
					w.Header().Set("Date", fixedNow.Format(http.TimeFormat))
					w.Header().Set("X-RateLimit-Remaining", "0")
					w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetTime.Unix(), 10))
					w.WriteHeader(http.StatusForbidden)
				}
			},
			timeout:         APITimeout,
			wantErrContains: "resets in 41m0s, but only ",
			wantRequests:    1,
		},
		{
			name: "403 uses server Date header to compute remaining time when local clock is skewed",
			handler: func(reqCount *int) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					*reqCount++
					serverTime := fixedNow
					resetTime := serverTime.Add(25 * time.Minute)
					w.Header().Set("Date", serverTime.Format(http.TimeFormat))
					w.Header().Set("X-RateLimit-Remaining", "0")
					w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetTime.Unix(), 10))
					w.WriteHeader(http.StatusForbidden)
				}
			},
			clientNow:       fixedNow.Add(-2 * time.Hour),
			timeout:         APITimeout,
			wantErrContains: "resets in 25m0s, but only ",
			wantRequests:    1,
		},
		{
			name: "403 falls back to local clock when Date header is unparseable",
			handler: func(reqCount *int) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					*reqCount++
					resetTime := fixedNow.Add(15 * time.Minute)
					w.Header().Set("Date", "invalid-date")
					w.Header().Set("X-RateLimit-Remaining", "0")
					w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetTime.Unix(), 10))
					w.WriteHeader(http.StatusForbidden)
				}
			},
			timeout:         APITimeout,
			wantErrContains: "resets in 15m0s, but only ",
			wantRequests:    1,
		},
		{
			name: "plain 404 returns immediately",
			handler: func(reqCount *int) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					*reqCount++
					w.WriteHeader(http.StatusNotFound)
				}
			},
			wantStatus:   http.StatusNotFound,
			wantRequests: 1,
		},
		{
			name: "persistent 502 exhausts retries and returns final response",
			handler: func(reqCount *int) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					*reqCount++
					w.WriteHeader(http.StatusBadGateway)
				}
			},
			wantStatus:   http.StatusBadGateway,
			wantRequests: len(retryBackoffs) + 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.clientNow.IsZero() {
				subNow := tt.clientNow
				nowFunc = func() time.Time { return subNow }
				defer func() { nowFunc = func() time.Time { return fixedNow } }()
			}

			var reqCount int
			server := httptest.NewServer(tt.handler(&reqCount))
			defer server.Close()

			ctx := scoped.WithHTTPClient(t.Context(), server.Client())
			if tt.timeout != 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.timeout)
				defer cancel()
			}

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}

			resp, err := Do(ctx, req)
			if resp != nil {
				defer CloseResponseBody(ctx, resp)
			}

			if reqCount != tt.wantRequests {
				t.Errorf("got %d requests, want %d", reqCount, tt.wantRequests)
			}

			if tt.wantErrMsg != "" {
				if err == nil {
					t.Fatalf("expected error %q, got nil", tt.wantErrMsg)
				}
				if err.Error() != tt.wantErrMsg {
					t.Errorf("got error %q, want %q", err.Error(), tt.wantErrMsg)
				}
				if !IsRateLimitError(err) {
					t.Errorf("expected IsRateLimitError(err) == true, got false")
				}
			} else if tt.wantErrContains != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErrContains)
				}
				if !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Errorf("got error %q, want containing %q", err.Error(), tt.wantErrContains)
				}
				if !strings.Contains(err.Error(), "remain before the deadline") {
					t.Errorf("got error %q, want containing %q", err.Error(), "remain before the deadline")
				}
				if !IsRateLimitError(err) {
					t.Errorf("expected IsRateLimitError(err) == true, got false")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if resp == nil {
					t.Fatal("expected non-nil response, got nil")
				}
				if resp.StatusCode != tt.wantStatus {
					t.Errorf("got status %d, want %d", resp.StatusCode, tt.wantStatus)
				}
			}
		})
	}
}

func TestDoGitHubRequest_Cache(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("AP_CACHE_DIR", cacheDir)

	var (
		serverETag     string
		serverBody     string
		gotIfNoneMatch string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIfNoneMatch = r.Header.Get("If-None-Match")
		if serverETag != "" && gotIfNoneMatch == serverETag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if serverETag != "" {
			w.Header().Set("ETag", serverETag)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(serverBody))
	}))
	defer server.Close()

	ctx := scoped.WithHTTPClient(t.Context(), server.Client())
	hash := sha256.Sum256([]byte(server.URL))
	expectedCacheFile := filepath.Join(cacheDir, "github", hex.EncodeToString(hash[:])+".json")

	tests := []struct {
		name            string
		setupServer     func()
		wantIfNoneMatch string
		wantBody        string
		wantCacheETag   string
	}{
		{
			name: "first GET without If-None-Match -> 200 + ETag, entry written",
			setupServer: func() {
				serverETag = "\"etag-1\""
				serverBody = `{"version":"1.0"}`
			},
			wantIfNoneMatch: "",
			wantBody:        `{"version":"1.0"}`,
			wantCacheETag:   "\"etag-1\"",
		},
		{
			name: "second GET sends If-None-Match -> server answers 304 -> caller receives cached body",
			setupServer: func() {
				// Server state unchanged, will respond with 304
			},
			wantIfNoneMatch: "\"etag-1\"",
			wantBody:        `{"version":"1.0"}`,
			wantCacheETag:   "\"etag-1\"",
		},
		{
			name: "200 with new ETag replaces entry",
			setupServer: func() {
				serverETag = "\"etag-2\""
				serverBody = `{"version":"2.0"}`
			},
			wantIfNoneMatch: "\"etag-1\"",
			wantBody:        `{"version":"2.0"}`,
			wantCacheETag:   "\"etag-2\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupServer()

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}

			resp, err := Do(ctx, req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			defer CloseResponseBody(ctx, resp)

			if gotIfNoneMatch != tt.wantIfNoneMatch {
				t.Errorf("got If-None-Match %q, want %q", gotIfNoneMatch, tt.wantIfNoneMatch)
			}

			if resp.StatusCode != http.StatusOK {
				t.Errorf("got status %d, want %d", resp.StatusCode, http.StatusOK)
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("failed to read response body: %v", err)
			}
			if string(body) != tt.wantBody {
				t.Errorf("got body %q, want %q", string(body), tt.wantBody)
			}

			data, err := os.ReadFile(expectedCacheFile)
			if err != nil {
				t.Fatalf("failed to read cache file %s: %v", expectedCacheFile, err)
			}

			var entry cacheEntry
			if err := json.Unmarshal(data, &entry); err != nil {
				t.Fatalf("failed to parse cache entry: %v", err)
			}

			if entry.ETag != tt.wantCacheETag {
				t.Errorf("cached ETag = %q, want %q", entry.ETag, tt.wantCacheETag)
			}
			if entry.URL != server.URL {
				t.Errorf("cached URL = %q, want %q", entry.URL, server.URL)
			}
			if entry.Status != http.StatusOK {
				t.Errorf("cached Status = %d, want %d", entry.Status, http.StatusOK)
			}
			if string(entry.bodyBytes()) != tt.wantBody {
				t.Errorf("cached body = %q, want %q", string(entry.bodyBytes()), tt.wantBody)
			}
			if entry.FetchedAt == "" {
				t.Errorf("cached FetchedAt is empty")
			}
		})
	}
}

func TestParseNextLink(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{
			name:   "empty",
			header: "",
			want:   "",
		},
		{
			name:   "single next link",
			header: `<https://api.github.com/releases?page=2>; rel="next"`,
			want:   "https://api.github.com/releases?page=2",
		},
		{
			name:   "next and last links",
			header: `<https://api.github.com/releases?page=2>; rel="next", <https://api.github.com/releases?page=5>; rel="last"`,
			want:   "https://api.github.com/releases?page=2",
		},
		{
			name:   "prev and first without next",
			header: `<https://api.github.com/releases?page=1>; rel="prev", <https://api.github.com/releases?page=1>; rel="first"`,
			want:   "",
		},
		{
			name:   "unquoted rel",
			header: `<https://api.github.com/releases?page=3>; rel=next`,
			want:   "https://api.github.com/releases?page=3",
		},
		{
			name:   "whitespace and extra parameters",
			header: ` <https://api.github.com/releases?page=4> ; title="Next" ; rel = "next" `,
			want:   "https://api.github.com/releases?page=4",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseNextLink(tc.header)
			if got != tc.want {
				t.Errorf("parseNextLink(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

func TestListReleases(t *testing.T) {
	page1 := []Release{{TagName: "v1.2.0"}, {TagName: "v1.1.0"}}
	page2 := []Release{{TagName: "v1.0.0"}, {TagName: "v0.9.0"}}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_ = json.NewEncoder(w).Encode(page2)
			return
		}
		w.Header().Set("Link", fmt.Sprintf("<http://%s%s?page=2>; rel=\"next\"", r.Host, r.URL.Path))
		_ = json.NewEncoder(w).Encode(page1)
	}))
	defer server.Close()

	ctx := scoped.WithHTTPClient(t.Context(), server.Client())

	t.Run("all pages when max is 0", func(t *testing.T) {
		rels, err := ListReleases(ctx, server.URL, "test/repo", 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(rels) != 4 {
			t.Fatalf("got %d releases, want 4", len(rels))
		}
		if rels[0].TagName != "v1.2.0" || rels[3].TagName != "v0.9.0" {
			t.Errorf("unexpected releases: %v", rels)
		}
	})

	t.Run("capped by max", func(t *testing.T) {
		rels, err := ListReleases(ctx, server.URL, "test/repo", 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(rels) != 3 {
			t.Fatalf("got %d releases, want 3", len(rels))
		}
		if rels[2].TagName != "v1.0.0" {
			t.Errorf("unexpected 3rd release: %v", rels[2])
		}
	})
}

func TestGetText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello text content"))
	}))
	defer server.Close()

	ctx := scoped.WithHTTPClient(t.Context(), server.Client())
	text, err := GetText(ctx, server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "hello text content" {
		t.Errorf("got %q, want %q", text, "hello text content")
	}
}
