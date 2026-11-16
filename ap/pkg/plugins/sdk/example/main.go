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
	"path/filepath"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins/sdk"
)

func main() {
	if err := sdk.ServeStdio(sdk.Plugin{
		Name:       "example",
		Activation: []string{"example.conf"},
		Tasks: []sdk.Task{
			{
				Name:        "append",
				Verb:        "build",
				Description: "Copies input file to output with an appended line",
				Outputs:     []string{"output.txt"},
				Run: func(ctx context.Context, task sdk.TaskContext) error {
					inputBytes, err := os.ReadFile(filepath.Join(task.Dir, "input.txt"))
					if err != nil {
						return fmt.Errorf("reading input.txt: %w", err)
					}
					if _, err := fmt.Fprintln(task.Stdout, "processed input.txt"); err != nil {
						return fmt.Errorf("writing stdout: %w", err)
					}
					output := append(inputBytes, []byte("appended line\n")...)
					if err := os.WriteFile(filepath.Join(task.OutputDir, "output.txt"), output, 0644); err != nil {
						return fmt.Errorf("writing output.txt: %w", err)
					}
					return nil
				},
			},
		},
	}); err != nil {
		fmt.Fprintf(os.Stderr, "example plugin: %v\n", err)
		os.Exit(1)
	}
}
