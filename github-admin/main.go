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

	return rootCmd.ExecuteContext(ctx)
}
