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

package scoped

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/klog/v2"
)

type contextKey int

const (
	httpClientKey contextKey = iota
	nowKey
	logKey
)

// Logging levels for verbosity 1 and 2 (verbosity n is slog.Level(-n)).
// Verbosity 4 is slog.LevelDebug (-4).
const (
	LevelVerbose1 slog.Level = -1
	LevelVerbose2 slog.Level = -2
)

var defaultHTTPClient = func() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{Transport: t}
}()

// WithHTTPClient returns a copy of ctx carrying the HTTP client c.
func WithHTTPClient(ctx context.Context, c *http.Client) context.Context {
	return context.WithValue(ctx, httpClientKey, c)
}

// HTTPClient returns the HTTP client associated with ctx, or a default
// client with a response-header timeout if none was set. It never returns nil.
func HTTPClient(ctx context.Context) *http.Client {
	if c, ok := ctx.Value(httpClientKey).(*http.Client); ok && c != nil {
		return c
	}
	return defaultHTTPClient
}

// WithLog returns a copy of ctx carrying the logger.
func WithLog(ctx context.Context, log *slog.Logger) context.Context {
	ctx = context.WithValue(ctx, logKey, log)
	return klog.NewContext(ctx, logr.FromSlogHandler(log.Handler()))
}

// Log returns the logger associated with ctx, or the default logger
// if none was set.
func Log(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(logKey).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// WithNow returns a copy of ctx carrying the clock function now.
func WithNow(ctx context.Context, now func() time.Time) context.Context {
	return context.WithValue(ctx, nowKey, now)
}

// Now returns the current time using the clock function in ctx,
// or time.Now() if none was set.
func Now(ctx context.Context) time.Time {
	if now, ok := ctx.Value(nowKey).(func() time.Time); ok && now != nil {
		return now()
	}
	return time.Now()
}
