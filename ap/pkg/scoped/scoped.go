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

// Logger is a structured logger with named verbosity levels.
type Logger interface {
	Error(message string, keysAndValues ...any)
	Warn(message string, keysAndValues ...any)
	Info(message string, keysAndValues ...any)
	Debug(message string, keysAndValues ...any)
	Fine(message string, keysAndValues ...any)
	Finer(message string, keysAndValues ...any)
	Finest(message string, keysAndValues ...any)
	With(keysAndValues ...any) Logger
}

type defaultLogger struct {
	logger *slog.Logger
}

// NewLogger returns a Logger backed by handler.
func NewLogger(handler slog.Handler) Logger {
	return &defaultLogger{
		logger: slog.New(handler),
	}
}

func (l *defaultLogger) log(level slog.Level, message string, keysAndValues ...any) {
	l.logger.Log(context.Background(), level, message, keysAndValues...)
}

func (l *defaultLogger) Error(message string, keysAndValues ...any) {
	l.log(slog.LevelError, message, keysAndValues...)
}

func (l *defaultLogger) Warn(message string, keysAndValues ...any) {
	l.log(slog.LevelWarn, message, keysAndValues...)
}

func (l *defaultLogger) Info(message string, keysAndValues ...any) {
	l.log(slog.LevelInfo, message, keysAndValues...)
}

func (l *defaultLogger) Debug(message string, keysAndValues ...any) {
	l.log(slog.Level(-1), message, keysAndValues...)
}

func (l *defaultLogger) Fine(message string, keysAndValues ...any) {
	l.log(slog.Level(-2), message, keysAndValues...)
}

func (l *defaultLogger) Finer(message string, keysAndValues ...any) {
	l.log(slog.Level(-3), message, keysAndValues...)
}

func (l *defaultLogger) Finest(message string, keysAndValues ...any) {
	l.log(slog.Level(-4), message, keysAndValues...)
}

func (l *defaultLogger) With(keysAndValues ...any) Logger {
	return &defaultLogger{
		logger: l.logger.With(keysAndValues...),
	}
}

type logSink struct {
	logger Logger
}

var _ logr.LogSink = (*logSink)(nil)

func (s *logSink) Init(info logr.RuntimeInfo) {}

func (s *logSink) Enabled(level int) bool {
	return true
}

func (s *logSink) Info(level int, msg string, keysAndValues ...any) {
	switch {
	case level <= 0:
		s.logger.Info(msg, keysAndValues...)
	case level == 1:
		s.logger.Debug(msg, keysAndValues...)
	case level == 2:
		s.logger.Fine(msg, keysAndValues...)
	case level == 3:
		s.logger.Finer(msg, keysAndValues...)
	default:
		s.logger.Finest(msg, keysAndValues...)
	}
}

func (s *logSink) Error(err error, msg string, keysAndValues ...any) {
	kvs := append([]any{"error", err}, keysAndValues...)
	s.logger.Error(msg, kvs...)
}

func (s *logSink) WithValues(keysAndValues ...any) logr.LogSink {
	return &logSink{logger: s.logger.With(keysAndValues...)}
}

func (s *logSink) WithName(name string) logr.LogSink {
	return s
}

// WithLog returns a copy of ctx carrying the logger.
func WithLog(ctx context.Context, log Logger) context.Context {
	ctx = context.WithValue(ctx, logKey, log)
	return klog.NewContext(ctx, logr.New(&logSink{logger: log}))
}

// Log returns the logger associated with ctx, or the default logger
// if none was set.
func Log(ctx context.Context) Logger {
	if l, ok := ctx.Value(logKey).(Logger); ok {
		return l
	}
	return NewLogger(slog.Default().Handler())
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
