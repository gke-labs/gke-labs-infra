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
	"fmt"
	"os"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/droppederrors"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/fileset"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	golang "github.com/gke-labs/gke-labs-infra/ap/pkg/go"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"github.com/spf13/cobra"
	"golang.org/x/tools/go/analysis"
)

// DroppedErrorsOptions holds CLI options for the droppederrors command.
type DroppedErrorsOptions struct {
	*RootOptions
	Mode               string
	Baseline           string
	WriteBaseline      bool
	Exclude            []string
	SkipTests          bool
	SkipGenerated      bool
	UseDefaultExcludes bool
	GOOS               []string
}

// BuildDroppedErrorsCommand constructs the cobra command for "droppederrors".
func BuildDroppedErrorsCommand(rootOpt *RootOptions) *cobra.Command {
	opt := DroppedErrorsOptions{
		RootOptions:        rootOpt,
		SkipTests:          true,
		SkipGenerated:      false,
		UseDefaultExcludes: true,
	}

	cmd := &cobra.Command{
		Use:   "droppederrors [packages...]",
		Short: "Check for dropped errors (calls whose error result is ignored)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunDroppedErrors(cmd.Context(), cmd, opt, args)
		},
	}

	cmd.Flags().StringVar(&opt.Mode, "mode", "", "Mode: ignore | warn | error (default: error)")
	cmd.Flags().StringVar(&opt.Baseline, "baseline", "", "Path to baseline file")
	cmd.Flags().BoolVar(&opt.WriteBaseline, "write-baseline", false, "Write baseline of current dropped errors")
	cmd.Flags().StringSliceVar(&opt.Exclude, "exclude", nil, "Additional function symbols to exclude")
	cmd.Flags().BoolVar(&opt.SkipTests, "skip-tests", true, "Skip _test.go files")
	cmd.Flags().BoolVar(&opt.SkipGenerated, "skip-generated", false, "Skip generated files")
	cmd.Flags().BoolVar(&opt.UseDefaultExcludes, "use-default-excludes", true, "Use default standard library exclusions")
	cmd.Flags().StringSliceVar(&opt.GOOS, "goos", nil, "Target GOOS list to check")

	return cmd
}

// RunDroppedErrors executes the droppederrors command.
func RunDroppedErrors(ctx context.Context, cmd *cobra.Command, opt DroppedErrorsOptions, _ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current working directory: %w", err)
	}

	if opt.RootOptions == nil {
		opt.RootOptions = &RootOptions{}
	}

	repoRoot := opt.RootOptions.RepoRoot
	if repoRoot == "" {
		rRoot, err := findRoots()
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v; falling back to current directory\n", err)
			repoRoot = cwd
		} else {
			repoRoot = rRoot
		}
	}

	targetDir := repoRoot
	if opt.RootOptions.Dir != "" {
		targetDir = opt.RootOptions.Dir
	}

	var cfg *config.Config
	if targetDir != "" {
		c, err := config.Effective(repoRoot, targetDir)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		cfg = c
	}

	mode := opt.Mode
	if mode == "" {
		if cfg != nil {
			mode = cfg.DroppedErrorsMode()
		} else {
			mode = "error"
		}
	}
	if mode != "error" && mode != "warn" && mode != "ignore" {
		return fmt.Errorf("invalid droppederrors mode %q: must be one of 'error', 'warn', 'ignore'", mode)
	}
	if mode == "ignore" && !opt.WriteBaseline {
		return nil
	}

	var exclude []string
	if cfg != nil {
		exclude = append(exclude, cfg.DroppedErrorsExclude()...)
	}
	exclude = append(exclude, opt.Exclude...)

	skipTests := opt.SkipTests
	if cmd != nil && !cmd.Flags().Changed("skip-tests") && cfg != nil {
		skipTests = cfg.DroppedErrorsSkipTests()
	}

	skipGenerated := opt.SkipGenerated
	if cmd != nil && !cmd.Flags().Changed("skip-generated") && cfg != nil {
		skipGenerated = cfg.DroppedErrorsSkipGenerated()
	}

	useDefaultExcludes := opt.UseDefaultExcludes
	if cmd != nil && !cmd.Flags().Changed("use-default-excludes") && cfg != nil {
		useDefaultExcludes = cfg.DroppedErrorsUseDefaultExcludes()
	}

	var skipGlobs []string
	if cfg != nil {
		skipGlobs = cfg.Skip
	}

	goosList := opt.GOOS
	if len(goosList) == 0 && cfg != nil {
		goosList = cfg.LintGOOS()
	}

	policy := fileset.NewPolicy(skipGenerated, skipTests, skipGlobs, repoRoot)
	analyzer := droppederrors.NewAnalyzer(droppederrors.Config{
		Policy:             policy,
		Exclude:            exclude,
		UseDefaultExcludes: &useDefaultExcludes,
	})

	configuredBaseline := opt.Baseline
	if configuredBaseline == "" && cfg != nil {
		configuredBaseline = cfg.DroppedErrorsBaseline()
	}

	deSettings := &config.DroppedErrorsConfig{
		CommonLinterConfig: config.CommonLinterConfig{
			Mode:          mode,
			SkipGenerated: &skipGenerated,
			SkipTests:     &skipTests,
			Baseline:      configuredBaseline,
		},
		Exclude:            exclude,
		UseDefaultExcludes: &useDefaultExcludes,
	}

	task := &golang.AnalyzersTask{
		Dir:           cwd,
		RepoRoot:      repoRoot,
		Enabled:       map[string]codestyle.Mode{"droppederrors": codestyle.Mode(mode)},
		Policies:      map[string]fileset.Policy{"droppederrors": policy},
		Settings:      map[string]config.LinterConfig{"droppederrors": deSettings},
		Analyzers:     []*analysis.Analyzer{analyzer},
		GOOS:          goosList,
		WriteBaseline: opt.WriteBaseline,
		BaselinePath:  opt.Baseline,
	}

	if opt.RootOptions == nil {
		opt.RootOptions = &RootOptions{}
	}
	if opt.RepoRoot == "" {
		opt.RepoRoot = repoRoot
	}
	r, err := opt.GetSourceTree()
	if err != nil {
		return err
	}

	return task.Run(ctx, tasks.NewScope(repoRoot, cwd, r))
}
