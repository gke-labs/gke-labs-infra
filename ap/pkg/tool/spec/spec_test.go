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

package spec

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetchText(t *testing.T) {
	var count503 atomic.Int32
	var count404 atomic.Int32
	var countCancelled atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/retry-then-succeed":
			if count503.Add(1) == 1 {
				http.Error(w, "service unavailable", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte("v1.2.3\n"))
		case "/notfound":
			count404.Add(1)
			http.Error(w, "not found", http.StatusNotFound)
		case "/cancelled":
			countCancelled.Add(1)
			_, _ = w.Write([]byte("unexpected"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tests := []struct {
		name       string
		path       string
		makeCtx    func(t *testing.T) context.Context
		want       string
		wantErrSub string
		wantCount  int32
		getCount   func() int32
	}{
		{
			name: "succeeds after a 503 then 200",
			path: "/retry-then-succeed",
			makeCtx: func(t *testing.T) context.Context {
				return t.Context()
			},
			want:      "v1.2.3",
			wantCount: 2,
			getCount:  count503.Load,
		},
		{
			name: "does not retry a 404",
			path: "/notfound",
			makeCtx: func(t *testing.T) context.Context {
				return t.Context()
			},
			wantErrSub: "HTTP 404",
			wantCount:  1,
			getCount:   count404.Load,
		},
		{
			name: "returns promptly when the caller's context is already cancelled",
			path: "/cancelled",
			makeCtx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				return ctx
			},
			wantErrSub: "context canceled",
			wantCount:  0,
			getCount:   countCancelled.Load,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := tc.makeCtx(t)
			start := time.Now()
			got, err := fetchText(ctx, server.URL+tc.path)
			elapsed := time.Since(start)

			if tc.wantErrSub != "" {
				if err == nil {
					t.Fatalf("fetchText() expected error containing %q, got nil", tc.wantErrSub)
				}
				if !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Errorf("fetchText() error = %v, want substring %q", err, tc.wantErrSub)
				}
			} else {
				if err != nil {
					t.Fatalf("fetchText() unexpected error: %v", err)
				}
				if got != tc.want {
					t.Errorf("fetchText() = %q, want %q", got, tc.want)
				}
			}

			if tc.getCount != nil {
				if gotCount := tc.getCount(); gotCount != tc.wantCount {
					t.Errorf("request count = %d, want %d", gotCount, tc.wantCount)
				}
			}

			if tc.name == "returns promptly when the caller's context is already cancelled" {
				if elapsed > 100*time.Millisecond {
					t.Errorf("cancelled context took too long to return: %v", elapsed)
				}
			}
		})
	}
}
