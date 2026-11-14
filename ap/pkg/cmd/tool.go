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
	"fmt"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool"
	"github.com/spf13/cobra"
)

// BuildToolCommand constructs the cobra command for "tool".
func BuildToolCommand(rootOpt *RootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tool [subcommand]",
		Short: "Manage and run pinned developer tools",
	}

	for _, t := range tool.All() {
		cmd.AddCommand(buildToolSubcommand(rootOpt, t))
	}
	cmd.AddCommand(BuildToolInstallCommand(rootOpt))
	cmd.AddCommand(BuildToolUpdateVersionsCommand(rootOpt))

	return cmd
}

func buildToolSubcommand(rootOpt *RootOptions, t *tool.Tool) *cobra.Command {
	cmd := &cobra.Command{
		Use:                t.Name + " [--] [args...]",
		Short:              t.Short,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, rawArgs []string) error {
			raw := rawArgs
			dryRun := rootOpt.DryRun
			if len(raw) > 0 && raw[0] == "--dry-run" {
				dryRun = true
				raw = raw[1:]
			}
			if len(raw) > 0 && raw[0] == "--" {
				raw = raw[1:]
			}

			repoRoot := rootOpt.RepoRoot
			if repoRoot == "" {
				repoRoot = tool.FindRepoRoot()
			}

			return t.Run(cmd.Context(), tool.RunOptions{
				RepoRoot: repoRoot,
				Args:     raw,
				DryRun:   dryRun,
				Stdout:   cmd.OutOrStdout(),
				Stderr:   cmd.ErrOrStderr(),
				Stdin:    cmd.InOrStdin(),
			})
		},
	}
	return cmd
}

// BuildToolInstallCommand constructs the cobra command for "tool install".
func BuildToolInstallCommand(rootOpt *RootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install [dir]",
		Short: "Install symlinks for managed tools pointing to ap",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var dir string
			if len(args) > 0 {
				dir = args[0]
			}
			_, err := tool.Install(tool.InstallOptions{
				RepoRoot:  rootOpt.RepoRoot,
				TargetDir: dir,
				Stdout:    cmd.OutOrStdout(),
			})
			return err
		},
	}
	return cmd
}

// ToolUpdateVersionsOptions holds flags for "tool update-versions".
type ToolUpdateVersionsOptions struct {
	Protoc    bool
	Docker    bool
	Buildx    bool
	Kind      bool
	Kubectl   bool
	Kustomize bool
}

// BuildToolUpdateVersionsCommand constructs the cobra command for "tool update-versions".
func BuildToolUpdateVersionsCommand(rootOpt *RootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update-versions",
		Short: "Update baked-in tool release versions and checksums in versions.yaml",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRoot := rootOpt.RepoRoot
			if repoRoot == "" {
				var err error
				repoRoot, err = findRoots()
				if err != nil {
					return err
				}
			}

			// If any flag was explicitly set to true, only run tools that were explicitly set to true.
			// Otherwise (e.g. no flags or only false flags given), run all tools whose flag is true.
			anyExplicitlyEnabled := false
			for _, t := range tool.All() {
				if t.Update == nil {
					continue
				}
				flagName := t.UpdateFlag
				if flagName == "" {
					flagName = t.Name
				}
				if cmd.Flags().Changed(flagName) {
					enabled, err := cmd.Flags().GetBool(flagName)
					if err != nil {
						return err
					}
					if enabled {
						anyExplicitlyEnabled = true
						break
					}
				}
			}

			for _, t := range tool.All() {
				if t.Update == nil {
					continue
				}
				flagName := t.UpdateFlag
				if flagName == "" {
					flagName = t.Name
				}
				if anyExplicitlyEnabled && !cmd.Flags().Changed(flagName) {
					continue
				}
				enabled, err := cmd.Flags().GetBool(flagName)
				if err != nil {
					return err
				}
				if !enabled {
					continue
				}

				if err := t.Update(cmd.Context(), repoRoot); err != nil {
					return fmt.Errorf("failed updating %s versions: %w", t.Name, err)
				}
			}

			return nil
		},
	}

	for _, t := range tool.All() {
		if t.Update == nil {
			continue
		}
		flagName := t.UpdateFlag
		if flagName == "" {
			flagName = t.Name
		}
		desc := t.UpdateDesc
		if desc == "" {
			desc = fmt.Sprintf("Update %s versions in versions.yaml", t.Name)
		}
		cmd.Flags().Bool(flagName, true, desc)
	}

	return cmd
}
