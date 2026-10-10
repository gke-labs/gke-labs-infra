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
	"path/filepath"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/droppederrors"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/spf13/cobra"
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
func RunDroppedErrors(ctx context.Context, cmd *cobra.Command, opt DroppedErrorsOptions, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current working directory: %w", err)
	}

	repoRoot := ""
	apRoot := ""
	if opt.RootOptions != nil {
		repoRoot = opt.RootOptions.RepoRoot
		apRoot = opt.RootOptions.APRoot
	}
	if repoRoot == "" || apRoot == "" {
		rRoot, aRoot, err := findRoots()
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v; falling back to current directory\n", err)
			if repoRoot == "" {
				repoRoot = cwd
			}
			if apRoot == "" {
				apRoot = cwd
			}
		} else {
			if repoRoot == "" {
				repoRoot = rRoot
			}
			if apRoot == "" {
				apRoot = aRoot
			}
		}
	}

	if apRoot == "" {
		apRoot = cwd
	}
	if repoRoot == "" {
		repoRoot = apRoot
	}

	var cfg *config.Config
	if apRoot != "" {
		c, err := config.Load(apRoot)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		cfg = c
	}
	if cfg == nil && repoRoot != "" {
		c, err := config.Load(repoRoot)
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

	baseline := opt.Baseline
	if baseline == "" && cfg != nil {
		baseline = cfg.DroppedErrorsBaseline()
	}
	if baseline == "" {
		defaultBaseline := filepath.Join(apRoot, ".ap", "droppederrors-baseline.txt")
		exists, err := fileutils.FileExists(defaultBaseline)
		if err != nil {
			return err
		}
		if exists {
			baseline = defaultBaseline
		} else if repoRoot != "" {
			repoBaseline := filepath.Join(repoRoot, ".ap", "droppederrors-baseline.txt")
			exists, err := fileutils.FileExists(repoBaseline)
			if err != nil {
				return err
			}
			if exists {
				baseline = repoBaseline
			}
		}
		if baseline == "" && opt.WriteBaseline {
			baseline = defaultBaseline
		}
	}
	if baseline != "" && !filepath.IsAbs(baseline) {
		baseline = filepath.Join(apRoot, baseline)
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

	goosList := opt.GOOS
	if len(goosList) == 0 && cfg != nil {
		goosList = cfg.DroppedErrorsGOOS()
	}

	if mode == "ignore" && !opt.WriteBaseline {
		return nil
	}

	packages := args
	if len(packages) == 0 {
		packages = []string{"./..."}
	}

	var skipGlobs []string
	if cfg != nil {
		skipGlobs = cfg.Skip
	}

	checkOpts := droppederrors.Options{
		RepoRoot:           repoRoot,
		APRoot:             apRoot,
		Dir:                cwd,
		Packages:           packages,
		Mode:               mode,
		BaselinePath:       baseline,
		WriteBaseline:      opt.WriteBaseline,
		Exclude:            exclude,
		SkipTests:          skipTests,
		SkipGenerated:      skipGenerated,
		SkipGlobs:          skipGlobs,
		UseDefaultExcludes: &useDefaultExcludes,
		GOOS:               goosList,
	}

	res, err := droppederrors.Run(ctx, checkOpts)
	if err != nil {
		return err
	}

	if opt.WriteBaseline {
		fmt.Printf("Wrote %d baseline entries to %s\n", len(res.BaselineEntries), baseline)
		return nil
	}

	for _, stale := range res.StaleEntries {
		fmt.Printf("stale baseline entry: %s (remove from baseline)\n", stale)
	}

	if mode == "warn" {
		findingsToReport := res.NewFindings
		if len(res.BaselineEntries) == 0 {
			findingsToReport = res.Findings
		}
		for _, f := range findingsToReport {
			formatFindingMsg(f, "warn")
		}
		return nil
	}

	if mode == "error" {
		if len(res.NewFindings) > 0 {
			for _, f := range res.NewFindings {
				formatFindingMsg(f, "error")
			}
			return fmt.Errorf("droppederrors: %d new unchecked errors found", len(res.NewFindings))
		}
		return nil
	}

	return nil
}

func formatFindingMsg(f droppederrors.Finding, severity string) {
	desc := "unchecked error"
	if f.Category == droppederrors.CategoryErrorOnlyCheckedForSuccess {
		desc = "error only checked for success"
	}
	if severity == "warn" {
		fmt.Printf("%s:%d:%d: [warn] %s in %s: %s\n", f.Pos.Filename, f.Pos.Line, f.Pos.Column, desc, f.EnclosingFunc, f.Callee)
	} else {
		fmt.Printf("%s:%d:%d: %s in %s: %s\n", f.Pos.Filename, f.Pos.Line, f.Pos.Column, desc, f.EnclosingFunc, f.Callee)
	}
}
