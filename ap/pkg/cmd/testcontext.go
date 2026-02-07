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
	"os"

	"github.com/gke-labs/gke-labs-infra/codestyle/pkg/testcontext"
	"github.com/spf13/cobra"
	"golang.org/x/tools/go/analysis/multichecker"
)

// BuildTestContextCommand constructs the cobra command for "testcontext".
// This is a hidden command used by "ap lint" to run the testcontext analyzer.
func BuildTestContextCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                "testcontext",
		Short:              "Run the testcontext analyzer",
		Hidden:             true,
		DisableFlagParsing: true,
		Run: func(_ *cobra.Command, args []string) {
			// multichecker.Main expects the first argument to be the program name,
			// and subsequent arguments to be flags and packages.
			// Since this is a subcommand, we need to shift the arguments.
			os.Args = append([]string{os.Args[0]}, args...)
			multichecker.Main(testcontext.Analyzer)
		},
	}

	return cmd
}
