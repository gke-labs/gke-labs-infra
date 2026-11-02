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

	"github.com/spf13/cobra"
)

// BuildBashCommand constructs the cobra command for "bash".
func BuildBashCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                "bash <script> [args...]",
		Short:              "Run a bash script from the repository root with managed tools on PATH",
		DisableFlagParsing: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return nil
		},
		RunE: func(cmd *cobra.Command, rawArgs []string) error {
			args := rawArgs
			if len(args) == 0 || (len(args) == 1 && (args[0] == "-h" || args[0] == "--help")) {
				return cmd.Help()
			}
			if len(args) > 0 && args[0] == "--" {
				args = args[1:]
			}
			if len(args) < 1 {
				return fmt.Errorf("requires at least 1 argument: <script> [args...]")
			}
			return RunScript(cmd.Context(), "bash", args[0], args[1:])
		},
	}
	return cmd
}
