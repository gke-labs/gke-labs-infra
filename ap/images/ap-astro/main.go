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

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins/sdk"
)

const astroExecutable = "/app/node_modules/.bin/astro"

func main() {
	if err := sdk.ServeStdio(sdk.Plugin{
		Name:       "astro",
		Activation: []string{"**/astro.config.*"},
		Tasks: []sdk.Task{
			{
				Name:        "build",
				Verb:        "build",
				Description: "Build Astro documentation site",
				Outputs:     []string{"dist"},
				Run: func(ctx context.Context, task sdk.TaskContext) error {
					outputDirectory := filepath.Join(task.OutputDir, "dist")
					command := exec.CommandContext(ctx, astroExecutable, "build", "--outDir", outputDirectory)
					command.Dir = task.Dir
					command.Stdout = task.Stdout
					command.Stderr = task.Stderr

					if err := command.Run(); err != nil {
						return fmt.Errorf("running astro build: %w", err)
					}
					return nil
				},
			},
		},
	}); err != nil {
		fmt.Fprintf(os.Stderr, "astro plugin: %v\n", err)
		os.Exit(1)
	}
}
