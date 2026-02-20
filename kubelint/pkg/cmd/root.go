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

	"github.com/gke-labs/gke-labs-infra/kubelint/pkg/manifests"
	"github.com/gke-labs/gke-labs-infra/kubelint/pkg/rules"
	"github.com/spf13/cobra"
)

func BuildRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "kubelint [file...]",
		Short:         "kubelint is a linter for Kubernetes manifests",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("no files specified")
			}

			allRules := rules.AllRules()
			var allDiagnostics []rules.Diagnostic

			for _, arg := range args {
				err := filepath.Walk(arg, func(path string, info os.FileInfo, err error) error {
					if err != nil {
						return err
					}
					if info.IsDir() {
						return nil
					}
					ext := filepath.Ext(path)
					if ext != ".yaml" && ext != ".yml" {
						return nil
					}

					f, err := os.Open(path)
					if err != nil {
						return fmt.Errorf("failed to open %s: %w", path, err)
					}
					defer f.Close()

					objs, err := manifests.Parse(f)
					if err != nil {
						return fmt.Errorf("failed to parse %s: %w", path, err)
					}

					for _, obj := range objs {
						for _, rule := range allRules {
							diags := rule.Check(obj)
							for i := range diags {
								diags[i].Message = fmt.Sprintf("%s:%d: %s [%s]", path, diags[i].Line, diags[i].Message, diags[i].RuleName)
								allDiagnostics = append(allDiagnostics, diags[i])
							}
						}
					}
					return nil
				})
				if err != nil {
					return err
				}
			}

			if len(allDiagnostics) > 0 {
				for _, d := range allDiagnostics {
					fmt.Fprintln(os.Stderr, d.Message)
				}
				return fmt.Errorf("lint failures found")
			}

			return nil
		},
	}

	return cmd
}

func Execute(ctx context.Context) error {
	rootCmd := BuildRootCommand()
	return rootCmd.ExecuteContext(ctx)
}
