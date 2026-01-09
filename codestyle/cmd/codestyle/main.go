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
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"gke-labs-infra/codestyle/pkg/fileheaders"
	"gke-labs-infra/codestyle/pkg/gostyle"

	"k8s.io/klog/v2"
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()
	
	ctx := context.Background()

	files := flag.Args()

	if err := run(ctx, files); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, files []string) error {
	repoRoot, err := findRepoRoot()
	if err != nil {
		return err
	}
	
	if err := fileheaders.Run(ctx, repoRoot, files); err != nil {
		return fmt.Errorf("fileheaders failed: %w", err)
	}
	
	if err := gostyle.Run(ctx, repoRoot, files); err != nil {
		return fmt.Errorf("gostyle failed: %w", err)
	}

	return nil
}

// findRepoRoot attempts to find the root of the git repository
func findRepoRoot() (string, error) {
	startDir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	dir := startDir
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", fmt.Errorf("could not find git repository root (starting at %s)", startDir)
}
