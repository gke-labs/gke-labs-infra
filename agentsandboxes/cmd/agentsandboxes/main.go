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

	"github.com/gke-labs/gke-labs-infra/agentsandboxes"
	"github.com/spf13/cobra"
)

func main() {
	ctx := context.Background()
	if err := BuildCommand().ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}

func BuildCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agentsandboxes",
		Short: "CLI tool for managing agent sandboxes",
	}

	cmd.AddCommand(BuildListCommand())
	cmd.AddCommand(BuildCreateCommand())
	cmd.AddCommand(BuildDeleteCommand())

	return cmd
}

func BuildListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List sandboxes",
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxes, err := agentsandboxes.List(cmd.Context())
			if err != nil {
				return err
			}
			for _, s := range sandboxes {
				fmt.Println(s.Name)
			}
			return nil
		},
	}
}

func BuildCreateCommand() *cobra.Command {
	var image string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			_, err := agentsandboxes.New(name).WithImage(image).Create(cmd.Context())
			return err
		},
	}
	cmd.Flags().StringVar(&image, "image", "local/ap-golang:latest", "Image to use for the sandbox")
	return cmd
}

func BuildDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			return agentsandboxes.Delete(cmd.Context(), name)
		},
	}
}
