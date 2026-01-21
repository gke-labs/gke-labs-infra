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

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/gke-labs/gke-labs-infra/github-admin/pkg/commands"
	"github.com/spf13/cobra"
)

func main() {
	ctx := context.Background()
	if err := Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func Run(ctx context.Context) error {
	rootCmd := &cobra.Command{
		Use:   "github-admin",
		Short: "Tool to administer github repos",
	}

	rootCmd.AddCommand(commands.BuildUpdateRepoCommand())
	rootCmd.AddCommand(commands.BuildExportCommand())
	rootCmd.AddCommand(commands.BuildApplyCommand())

	return rootCmd.ExecuteContext(ctx)
}
