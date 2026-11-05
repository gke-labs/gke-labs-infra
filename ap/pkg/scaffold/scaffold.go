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

package scaffold

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

// InitOptions contains options for scaffolding the .ap directory.
type InitOptions struct {
	Dir             string
	License         string
	CopyrightHolder string
	Force           bool
}

// InitTask represents a task to scaffold the .ap directory.
type InitTask struct {
	Options InitOptions
}

// Run executes the scaffold init task.
func (t *InitTask) Run(ctx context.Context, scope *tasks.TaskContext) error {
	return Init(t.Options)
}

// GetName returns the name of the task.
func (t *InitTask) GetName() string {
	return "init"
}

// GetChildren returns the child tasks.
func (t *InitTask) GetChildren() []tasks.Task {
	return nil
}

// Init scaffolds the .ap directory and initial configuration files.
func Init(opt InitOptions) error {
	if opt.Dir == "" {
		return fmt.Errorf("target directory is required")
	}

	license := opt.License
	if license == "" {
		license = "apache-2.0"
	}
	if license != "apache-2.0" && license != "none" {
		return fmt.Errorf("unsupported license %q (supported: \"apache-2.0\", \"none\")", license)
	}

	apDir := filepath.Join(opt.Dir, ".ap")

	// If not forcing, check if any of the target files already exist
	filesToCheck := []string{"ap.yaml", "go.yaml", "headers.yaml"}
	if !opt.Force {
		var existing []string
		for _, f := range filesToCheck {
			path := filepath.Join(apDir, f)
			exists, err := fileutils.FileExists(path)
			if err != nil {
				return fmt.Errorf("failed to check %s: %w", path, err)
			}
			if exists {
				existing = append(existing, f)
			}
		}
		if len(existing) > 0 {
			return fmt.Errorf(".ap configuration already exists in %s (%s) (use --force to overwrite)", opt.Dir, strings.Join(existing, ", "))
		}
	}

	if err := os.MkdirAll(apDir, 0755); err != nil {
		return fmt.Errorf("failed to create %s: %w", apDir, err)
	}

	// 1. ap.yaml
	apYAML := "version: latest\n"
	if err := os.WriteFile(filepath.Join(apDir, "ap.yaml"), []byte(apYAML), 0644); err != nil {
		return fmt.Errorf("failed to write ap.yaml: %w", err)
	}

	// 2. go.yaml
	goYAML := "gofmt:\n  enabled: true\ngovet:\n  enabled: true\n"
	if err := os.WriteFile(filepath.Join(apDir, "go.yaml"), []byte(goYAML), 0644); err != nil {
		return fmt.Errorf("failed to write go.yaml: %w", err)
	}

	// 3. headers.yaml
	var headersYAML string
	if license == "none" {
		headersYAML = "license: none\n"
	} else {
		headersYAML = fmt.Sprintf("license: %s\ncopyrightHolder: %s\n", license, opt.CopyrightHolder)
	}
	if err := os.WriteFile(filepath.Join(apDir, "headers.yaml"), []byte(headersYAML), 0644); err != nil {
		return fmt.Errorf("failed to write headers.yaml: %w", err)
	}

	return nil
}
