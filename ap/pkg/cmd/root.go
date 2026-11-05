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
	Dir        string
	DryRun     bool
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

// resolveTarget resolves a single target path argument against the repository root and opt.Dir.
func (opt *RootOptions) resolveTarget(r *sourcetree.SourceTree, target string) (string, error) {
	baseDir := opt.Dir
	if baseDir == "" {
		baseDir = opt.RepoRoot
	}

	resolved := config.ResolveTargetPath(opt.RepoRoot, baseDir, target)
	resolved = strings.TrimSuffix(resolved, string(filepath.Separator)+"...")
	if resolved == opt.RepoRoot {
		return opt.RepoRoot, nil
	}

	if !r.DirExists(resolved) && !r.FileExists(resolved) && resolved != opt.RepoRoot {
		return "", fmt.Errorf("target path %q does not exist", target)
	}

	rel, err := filepath.Rel(opt.RepoRoot, resolved)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("target path %q is outside repository root", target)
	}

	return resolved, nil
}

// Resolve resolves the target subtree path based on args.
func (opt *RootOptions) Resolve(args []string) error {
	if opt.RepoRoot == "" {
		return fmt.Errorf("repo root is not set")
	}

	r, err := opt.GetSourceTree()
	if err != nil {
		return err
	}

	if len(args) == 0 {
		return nil
	}

	resolved, err := opt.resolveTarget(r, args[0])
	if err != nil {
		return err
	}

	opt.Dir = resolved
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
			repoRoot, err := findRoots()
			if err != nil {
				return err
			}
			opt.RepoRoot = repoRoot
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			opt.Dir = cwd
			return nil
		},
	}

	fs := cmd.PersistentFlags()
	fs.BoolVar(&opt.DryRun, "dry-run", false, "If true, just print the tasks that would be executed")

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
	cmd.AddCommand(BuildBashCommand())

	return cmd
}

// findRoots attempts to find the root of the git repository
func findRoots() (string, error) {
	return tool.FindRoots()
}

func requireRepoRoot(opt *RootOptions) error {
	if opt.RepoRoot == "" {
		return fmt.Errorf("this command must be run inside a git repository (or set REPO_ROOT)")
	}
	return nil
}

// Execute runs the root command.
func Execute(ctx context.Context) error {
	rootCmd := BuildRootCommand()
	return rootCmd.ExecuteContext(ctx)
}
