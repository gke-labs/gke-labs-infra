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

	"github.com/gke-labs/gke-labs-infra/ap/pkg/format"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/generate"
	golang "github.com/gke-labs/gke-labs-infra/ap/pkg/go"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/images"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/k8s"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sandbox"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/version"
	"k8s.io/klog/v2"
)

func main() {
	klog.InitFlags(nil)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flags] <command>\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "\nCommands:\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  test    Run tests\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  e2e     Run e2e tests\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  build   Build artifacts\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  deploy  Deploy artifacts\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  generate Run generation tasks\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  format  Run formatting tasks (alias: fmt)\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  alpha   Experimental commands (e.g. sandbox)\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  version Print version information\n")
		fmt.Fprintf(flag.CommandLine.Output(), "\nFlags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(1)
	}

	command := args[0]
	ctx := context.Background()

	root, err := findRepoRoot()
	if err != nil {
		klog.Exitf("Failed to find repo root: %v", err)
	}

	var cmdErr error
	switch command {
	case "test":
		cmdErr = runTest(ctx, root)
	case "e2e":
		cmdErr = runE2e(ctx, root)
	case "build":
		cmdErr = runBuild(ctx, root)
	case "deploy":
		cmdErr = runDeploy(ctx, root)
	case "generate":
		cmdErr = runGenerate(ctx, root)
		if cmdErr == nil {
			cmdErr = runFormat(ctx, root)
		}
	case "format", "fmt":
		cmdErr = runFormat(ctx, root)
	case "alpha":
		cmdErr = runAlpha(ctx, root, args[1:])
	case "version":
		cmdErr = runVersion(ctx, root)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		flag.Usage()
		os.Exit(1)
	}

	if cmdErr != nil {
		klog.Exitf("Command %s failed: %v", command, cmdErr)
	}
}

func runAlpha(ctx context.Context, root string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("alpha requires a subcommand (sandbox)")
	}
	switch args[0] {
	case "sandbox":
		return sandbox.Run(ctx, root, args[1:])
	default:
		return fmt.Errorf("unknown alpha subcommand: %s", args[0])
	}
}

func runTest(ctx context.Context, root string) error {
	if err := golang.Test(ctx, root); err != nil {
		return err
	}

	// Run test-* scripts (excluding test-e2e*)
	testTasks, err := tasks.FindTaskScripts(root, tasks.WithPrefix("test-"), tasks.WithExcludePrefix("test-e2e"))
	if err != nil {
		return fmt.Errorf("failed to discover test tasks: %w", err)
	}
	return tasks.Run(ctx, root, testTasks)
}

func runE2e(ctx context.Context, root string) error {
	// Run test-e2e* scripts
	e2eTasks, err := tasks.FindTaskScripts(root, tasks.WithPrefix("test-e2e"))
	if err != nil {
		return fmt.Errorf("failed to discover e2e tasks: %w", err)
	}

	if len(e2eTasks) == 0 {
		klog.Warning("No e2e tasks found (looking for dev/tasks/test-e2e*)")
		return nil
	}

	return tasks.Run(ctx, root, e2eTasks)
}

func runBuild(ctx context.Context, root string) error {
	return images.Build(ctx, root)
}

func runDeploy(ctx context.Context, root string) error {
	// Deploy typically also builds
	if err := images.Build(ctx, root); err != nil {
		return fmt.Errorf("build failed during deploy: %w", err)
	}
	return k8s.Deploy(ctx, root)
}

func runGenerate(ctx context.Context, root string) error {
	return generate.Run(ctx, root)
}

func runFormat(ctx context.Context, root string) error {
	return format.Run(ctx, root)
}

func runVersion(ctx context.Context, root string) error {
	return version.Run(ctx, root)
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
