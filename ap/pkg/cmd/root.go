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
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

// RootOptions holds the configuration for the root command.
type RootOptions struct {
	RepoRoot string
	APRoot   string
	APRoots  []string
}

// BuildRootCommand constructs the root cobra command.
func BuildRootCommand() *cobra.Command {
	var opt RootOptions

	cmd := &cobra.Command{
		Use:   "ap",
		Short: "ap is a tool for managing gke-labs projects",
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			repoRoot, apRoot, err := findRoots()
			if err == nil {
				opt.RepoRoot = repoRoot
				opt.APRoot = apRoot

				if repoRoot != "" {
					apRoots, err := config.FindAllAPRoots(repoRoot)
					if err != nil {
						return fmt.Errorf("failed to find all ap roots: %w", err)
					}
					opt.APRoots = apRoots
				}
			}
			return nil
		},
	}

	fs := cmd.PersistentFlags()
	klogFlags := flag.NewFlagSet("klog", flag.ContinueOnError)
	klog.InitFlags(klogFlags)
	fs.AddGoFlagSet(klogFlags)

	cmd.AddCommand(BuildTestCommand(&opt))
	cmd.AddCommand(BuildE2eCommand(&opt))
	cmd.AddCommand(BuildLintCommand(&opt))
	cmd.AddCommand(BuildBuildCommand(&opt))
	cmd.AddCommand(BuildDeployCommand(&opt))
	cmd.AddCommand(BuildGenerateCommand(&opt))
	cmd.AddCommand(BuildFormatCommand(&opt))
	cmd.AddCommand(BuildVersionBumpCommand(&opt))
	cmd.AddCommand(BuildAlphaCommand(&opt))
	cmd.AddCommand(BuildServeCommand(&opt))
	cmd.AddCommand(BuildVersionCommand(&opt))

	return cmd
}

// findRoots attempts to find the root of the git repository and the closest ap root
func findRoots() (string, string, error) {
	repoRoot := os.Getenv("REPO_ROOT")
	apRoot := os.Getenv("AP_ROOT")

	if repoRoot != "" && apRoot != "" {
		return repoRoot, apRoot, nil
	}

	startDir, err := os.Getwd()
	if err != nil {
		return "", "", err
	}

	dir := startDir
	for {
		if apRoot == "" {
			if _, err := os.Stat(filepath.Join(dir, ".ap")); err == nil {
				apRoot = dir
			}
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			if repoRoot == "" {
				repoRoot = dir
			}
			if apRoot == "" {
				apRoot = dir
			}
			return repoRoot, apRoot, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	if repoRoot == "" {
		repoRoot = os.Getenv("AP_ROOT")
	}

	if repoRoot != "" {
		return repoRoot, apRoot, nil
	}

	return "", "", fmt.Errorf("could not find git repository root (starting at %s)", startDir)
}

func requireRepoRoot(opt *RootOptions) error {
	if opt.RepoRoot == "" {
		return fmt.Errorf("this command must be run inside a git repository (or set REPO_ROOT or AP_ROOT)")
	}
	return nil
}

// Execute runs the root command.
func Execute(ctx context.Context) error {
	rootCmd := BuildRootCommand()
	return rootCmd.ExecuteContext(ctx)
}
