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
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/generate"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scaffold"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

// InitOptions holds the configuration for the "init" command.
type InitOptions struct {
	*RootOptions
	TargetDir       string
	License         string
	CopyrightHolder string
	Generate        bool
	Force           bool
}

// BuildInitCommand constructs the cobra command for "init".
func BuildInitCommand(rootOpt *RootOptions) *cobra.Command {
	opt := InitOptions{
		RootOptions: rootOpt,
		License:     "apache-2.0",
	}

	cmd := &cobra.Command{
		Use:   "init [path]",
		Short: "Initialize .ap/ configuration for a repository",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				opt.TargetDir = args[0]
			}
			return RunInit(cmd.Context(), opt)
		},
	}

	cmd.Flags().StringVar(&opt.License, "license", opt.License, "License type ('apache-2.0' or 'none')")
	cmd.Flags().StringVar(&opt.CopyrightHolder, "copyright-holder", "", "Copyright holder name (required if license is 'apache-2.0')")
	cmd.Flags().StringVar(&opt.CopyrightHolder, "copyright", "", "Alias for --copyright-holder")
	if err := cmd.Flags().MarkHidden("copyright"); err != nil {
		panic(err)
	}
	cmd.Flags().BoolVar(&opt.Generate, "generate", false, "Run 'ap generate' after initialization")
	cmd.Flags().BoolVar(&opt.Force, "force", false, "Overwrite existing configuration files in .ap")

	return cmd
}

// RunInit executes the business logic for the "init" command.
func RunInit(ctx context.Context, opt InitOptions) error {
	targetDir := opt.TargetDir
	if targetDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current working directory: %w", err)
		}
		targetDir = wd
	} else if !filepath.IsAbs(targetDir) {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current working directory: %w", err)
		}
		targetDir = filepath.Clean(filepath.Join(wd, targetDir))
	}

	if opt.License == "" {
		opt.License = "apache-2.0"
	}

	if opt.License != "apache-2.0" && opt.License != "none" {
		return fmt.Errorf("unsupported license %q (supported: \"apache-2.0\", \"none\")", opt.License)
	}

	// Interactive prompting if stdin and stdout are terminal and flags were omitted
	if isTerminal(os.Stdin) && isTerminal(os.Stdout) {
		reader := bufio.NewReader(os.Stdin)
		if opt.License == "" {
			fmt.Print("License [apache-2.0]: ")
			input, err := reader.ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("failed to read license input: %w", err)
			}
			input = strings.TrimSpace(input)
			if input != "" {
				opt.License = input
			} else {
				opt.License = "apache-2.0"
			}
		}
		if opt.License != "none" && opt.CopyrightHolder == "" {
			defaultHolder := detectGitUserName(targetDir)
			if defaultHolder != "" {
				fmt.Printf("Copyright holder [%s]: ", defaultHolder)
			} else {
				fmt.Print("Copyright holder: ")
			}
			input, err := reader.ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("failed to read copyright holder input: %w", err)
			}
			input = strings.TrimSpace(input)
			if input != "" {
				opt.CopyrightHolder = input
			} else if defaultHolder != "" {
				opt.CopyrightHolder = defaultHolder
			}
		}
	} else {
		// Non-interactive fallback: if copyright holder is empty and license is not none, try git config
		if opt.License != "none" && opt.CopyrightHolder == "" {
			if defaultHolder := detectGitUserName(targetDir); defaultHolder != "" {
				opt.CopyrightHolder = defaultHolder
			}
		}
	}

	task := &scaffold.InitTask{
		Options: scaffold.InitOptions{
			Dir:             targetDir,
			License:         opt.License,
			CopyrightHolder: opt.CopyrightHolder,
			Force:           opt.Force,
		},
	}

	var allTasks []tasks.Task
	allTasks = append(allTasks, task)

	var tree *sourcetree.SourceTree
	if opt.Generate || opt.DryRun {
		repoRoot := opt.RepoRoot
		if repoRoot == "" {
			repoRoot = targetDir
		}
		if opt.RootOptions != nil {
			if opt.RepoRoot == "" {
				opt.RepoRoot = repoRoot
			}
			var err error
			tree, err = opt.GetSourceTree()
			if err != nil {
				return err
			}
		}
		scopes := []*tasks.APScope{tasks.NewScope(repoRoot, targetDir, tree)}
		if opt.Generate {
			genTasks, err := generate.GenerateTasks(repoRoot, scopes)
			if err == nil && genTasks != nil {
				allTasks = append(allTasks, genTasks)
			}
		}
	}

	if opt.DryRun {
		return tasks.Run(ctx, tasks.NewScope(targetDir, targetDir, tree), allTasks, tasks.RunOptions{DryRun: true})
	}

	if err := task.Run(ctx, nil); err != nil {
		return err
	}

	fmt.Printf("Initialized .ap/ configuration in %s\n", targetDir)
	if opt.License != "none" && opt.CopyrightHolder == "" {
		fmt.Println("Note: copyrightHolder is empty in .ap/headers.yaml; please set it before generating file headers.")
	}

	if opt.Generate {
		repoRoot := opt.RepoRoot
		if repoRoot == "" {
			repoRoot = targetDir
		}
		genOpt := GenerateOptions{
			RootOptions: &RootOptions{
				RepoRoot: repoRoot,
				Dir:      targetDir,
				DryRun:   opt.DryRun,
			},
		}
		if err := RunGenerate(ctx, genOpt); err != nil {
			return fmt.Errorf("failed to run generate: %w", err)
		}
	} else {
		fmt.Println("Run 'ap generate' to create CI workflows and presubmit scripts.")
	}

	return nil
}

func isTerminal(f *os.File) bool {
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

func detectGitUserName(dir string) string {
	cmd := exec.Command("git", "config", "user.name")
	cmd.Dir = dir
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_CONFIG_") {
			env = append(env, e)
		}
	}
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		klog.V(2).Infof("detectGitUserName in %s failed: %v", dir, err)
		return ""
	}
	return strings.TrimSpace(string(out))
}
