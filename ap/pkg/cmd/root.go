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

	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

// RootOptions holds the configuration for the root command.
type RootOptions struct {
	RepoRoot string
}

// BuildRootCommand constructs the root cobra command.
func BuildRootCommand() *cobra.Command {
	var opt RootOptions

	cmd := &cobra.Command{
		Use:   "ap",
		Short: "ap is a tool for managing gke-labs projects",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			root, err := findRepoRoot()
			if err == nil {
				opt.RepoRoot = root
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

// findRepoRoot attempts to find the root of the git repository
func findRepoRoot() (string, error) {
	if root := os.Getenv("AP_ROOT"); root != "" {
		return root, nil
	}

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

func requireRepoRoot(opt *RootOptions) error {
	if opt.RepoRoot == "" {
		return fmt.Errorf("this command must be run inside a git repository (or set AP_ROOT)")
	}
	return nil
}

// Execute runs the root command.
func Execute(ctx context.Context) error {
	rootCmd := BuildRootCommand()
	return rootCmd.ExecuteContext(ctx)
}
