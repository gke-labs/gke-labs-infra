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
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

// RootOptions holds the configuration for the root command.
type RootOptions struct {
	RepoRoot   string
	APRoot     string
	APRoots    []string
	DryRun     bool
	RootFlag   string
	SourceTree *sourcetree.SourceTree
}

// GetSourceTree returns the source tree associated with the root options.
func (opt *RootOptions) GetSourceTree() (*sourcetree.SourceTree, error) {
	if opt.SourceTree != nil {
		return opt.SourceTree, nil
	}
	if opt.RepoRoot == "" {
		return nil, fmt.Errorf("repo root is not set")
	}
	cfg, err := config.Load(opt.RepoRoot)
	if err != nil {
		return nil, err
	}
	var skips []string
	if cfg != nil {
		skips = cfg.Skip
	}
	r, err := sourcetree.Open(opt.RepoRoot, sourcetree.WithSkip(skips...))
	if err != nil {
		return nil, err
	}
	opt.SourceTree = r
	return r, nil
}

// ResolveRoot handles validating and resolving opt.RootFlag if specified.
func (opt *RootOptions) ResolveRoot() error {
	if opt.RootFlag == "" {
		return nil
	}
	absRoot := opt.RootFlag
	if !filepath.IsAbs(absRoot) {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current working directory: %w", err)
		}
		absRoot = filepath.Join(wd, opt.RootFlag)
	}
	fi, err := os.Stat(absRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("specified root %q does not exist", opt.RootFlag)
		}
		return fmt.Errorf("failed to stat specified root %q: %w", opt.RootFlag, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("specified root %q is not a directory", opt.RootFlag)
	}
	if _, err := os.Stat(filepath.Join(absRoot, ".ap")); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("specified root %q is not an AP root (missing .ap directory)", opt.RootFlag)
		}
		return fmt.Errorf("failed to check for .ap directory in %q: %w", opt.RootFlag, err)
	}
	opt.APRoots = []string{absRoot}
	opt.APRoot = absRoot

	// If absRoot is outside RepoRoot, anchor RepoRoot to absRoot
	if opt.RepoRoot == "" || (!strings.HasPrefix(absRoot, opt.RepoRoot+string(filepath.Separator)) && absRoot != opt.RepoRoot) {
		opt.RepoRoot = absRoot
		opt.SourceTree = nil
	}
	return nil
}

// Resolve resolves the target paths based on args.
func (opt *RootOptions) Resolve(args []string) error {
	if opt.RepoRoot == "" {
		return fmt.Errorf("repo root is not set")
	}

	if opt.RootFlag != "" {
		return opt.ResolveRoot()
	}

	r, err := opt.GetSourceTree()
	if err != nil {
		return err
	}

	roots, err := config.ResolveTargets(r, opt.RepoRoot, opt.APRoot, args)
	if err != nil {
		return err
	}
	opt.APRoots = roots
	return nil
}

// BuildRootCommand constructs the root cobra command.
func BuildRootCommand() *cobra.Command {
	var opt RootOptions

	cmd := &cobra.Command{
		Use:   "ap",
		Short: "ap is a tool for managing gke-labs projects",
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			repoRoot, apRoot, err := findRoots()
			if err != nil {
				return err
			}
			opt.RepoRoot = repoRoot
			opt.APRoot = apRoot
			return nil
		},
	}

	fs := cmd.PersistentFlags()
	fs.BoolVar(&opt.DryRun, "dry-run", false, "If true, just print the tasks that would be executed")
	fs.StringVar(&opt.RootFlag, "root", "", "The specific AP root to operate on")

	klogFlags := flag.NewFlagSet("klog", flag.ContinueOnError)
	klog.InitFlags(klogFlags)
	fs.AddGoFlagSet(klogFlags)

	cmd.AddCommand(BuildInitCommand(&opt))
	cmd.AddCommand(BuildLsCommand(&opt))
	cmd.AddCommand(BuildTestCommand(&opt))
	cmd.AddCommand(BuildE2eCommand(&opt))
	cmd.AddCommand(BuildLintCommand(&opt))
	cmd.AddCommand(BuildBuildCommand(&opt))
	cmd.AddCommand(BuildDeployCommand(&opt))
	cmd.AddCommand(BuildGenerateCommand(&opt))
	cmd.AddCommand(BuildVerifyGenerateCommand(&opt))
	cmd.AddCommand(BuildFormatCommand(&opt))
	cmd.AddCommand(BuildVersionBumpCommand(&opt))
	cmd.AddCommand(BuildAlphaCommand(&opt))
	cmd.AddCommand(BuildServeCommand(&opt))
	cmd.AddCommand(BuildVersionCommand(&opt))
	cmd.AddCommand(BuildToolCommand(&opt))
	cmd.AddCommand(BuildRunCommand())

	return cmd
}

// findRoots attempts to find the root of the git repository and the closest ap root
func findRoots() (string, string, error) {
	return tool.FindRoots()
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
