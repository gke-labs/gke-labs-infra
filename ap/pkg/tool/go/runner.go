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

package gotool

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
)

// RunGo executes the pinned Go binary with GOROOT set to the install directory and GOTOOLCHAIN=local.
func RunGo(ctx context.Context, version string, args []string, stdout, stderr io.Writer, stdin io.Reader) error {
	goBin, err := EnsureGo(ctx, version)
	if err != nil {
		return fmt.Errorf("failed to prepare go: %w", err)
	}

	installDir := filepath.Dir(filepath.Dir(goBin))

	cmd := exec.CommandContext(ctx, goBin, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = stdin

	env := os.Environ()
	var filteredEnv []string
	for _, e := range env {
		if strings.HasPrefix(e, "GOROOT=") || strings.HasPrefix(e, "GOTOOLCHAIN=") {
			continue
		}
		filteredEnv = append(filteredEnv, e)
	}
	filteredEnv = append(filteredEnv, "GOROOT="+installDir, "GOTOOLCHAIN=local")
	cmd.Env = fileutils.PrependPath(filteredEnv, filepath.Dir(goBin))

	return cmd.Run()
}
