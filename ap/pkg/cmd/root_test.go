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
	"io"
	"os"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"k8s.io/klog/v2"
)

func TestCommandLogging(t *testing.T) {
	var buf bytes.Buffer
	klog.SetLoggerWithOptions(tasks.NewLogger(&buf), klog.ContextualLogger(true))
	defer func() {
		klog.SetLoggerWithOptions(tasks.NewLogger(os.Stderr), klog.ContextualLogger(true))
	}()

	cmd := BuildRootCommand()
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"version"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	logger := scoped.Log(cmd.Context())
	logger.Info("command started", "component", "root", "target", "local")
	logger.V(2).Info("verbose debug message", "should", "hide")

	got := strings.TrimSpace(buf.String())
	want := "command started component=root target=local"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	buf.Reset()
	rootCmd := BuildRootCommand()
	rootCmd.SetOut(io.Discard)
	rootCmd.SetArgs([]string{"bash", "-h"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	bashCmd, _, err := rootCmd.Find([]string{"bash"})
	if err != nil {
		t.Fatal(err)
	}

	bashLogger := scoped.Log(bashCmd.Context())
	bashLogger.Info("bash is older than 4; scripts run in CI with bash 5. Install a newer bash (e.g. brew install bash) and make sure it is first on PATH.",
		"level", "warning", "path", "/bin/bash", "version", "3.2.57(1)-release")

	gotBash := strings.TrimSpace(buf.String())
	wantBash := "bash is older than 4; scripts run in CI with bash 5. Install a newer bash (e.g. brew install bash) and make sure it is first on PATH. level=warning path=/bin/bash version=3.2.57(1)-release"
	if gotBash != wantBash {
		t.Errorf("got %q, want %q", gotBash, wantBash)
	}
}
