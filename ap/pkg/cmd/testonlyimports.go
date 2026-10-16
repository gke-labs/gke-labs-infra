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

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/testonlyimports"
	"github.com/spf13/cobra"
	"golang.org/x/tools/go/analysis/multichecker"
)

// BuildTestOnlyImportsCommand constructs the cobra command for "testonlyimports".
// This is a hidden command used by "ap lint" to run the testonlyimports analyzer.
func BuildTestOnlyImportsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                "testonlyimports",
		Short:              "Run the testonlyimports analyzer",
		Hidden:             true,
		DisableFlagParsing: true,
		Run: func(_ *cobra.Command, args []string) {
			os.Args = append([]string{os.Args[0]}, args...)
			multichecker.Main(testonlyimports.Analyzer)
		},
	}

	return cmd
}
