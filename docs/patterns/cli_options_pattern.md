# Options Pattern for CLIs

This document describes the "Options Pattern" for structuring Command Line Interface (CLI) applications, particularly those using [Cobra](https://github.com/spf13/cobra).

## Overview

The goal of this pattern is to decouple the CLI command definition (flags, usage, argument parsing) from the actual business logic of the command. This improves testability and allows the business logic to be invoked programmatically if needed.

## Structure

The pattern generally involves:

1.  **Options Struct**: A struct to hold all the configuration/parameters for the command.
2.  **InitDefaults**: A method on the Options struct to set default values.
3.  **BuildCommand**: A function that constructs the `cobra.Command`. It binds flags to the Options struct.
4.  **Run Function**: A separate function that takes a Context and the populated Options struct to execute the logic.

## Example

### `main.go`

The entry point delegates to a `Run` function that sets up the root command.

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/gke-labs/gke-labs-infra/pkg/commands"
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
	rootCmd := &cobra.Command{Use: "app"}
	rootCmd.AddCommand(commands.BuildFooCommand())
	// ... add other commands
	return rootCmd.ExecuteContext(ctx)
}
```

### `pkg/commands/foo.go`

The command implementation follows the pattern:

```go
package commands

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

// FooOptions holds the configuration for the "foo" command.
type FooOptions struct {
	// Bar is a parameter for the command.
	Bar string
}

// InitDefaults sets the default values for FooOptions.
func (o *FooOptions) InitDefaults() {
	o.Bar = "bar1"
}

// BuildFooCommand constructs the cobra command for "foo".
func BuildFooCommand() *cobra.Command {
	var opt FooOptions
	opt.InitDefaults()

	cmd := &cobra.Command{
		Use:   "foo",
		Short: "A description of foo",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validation of flags or args can happen here if strictly related to CLI parsing,
			// but business logic validation belongs in RunFoo.
			return RunFoo(cmd.Context(), opt)
		},
	}

	// Bind flags to the options struct
	cmd.Flags().StringVar(&opt.Bar, "bar", opt.Bar, "The bar to use")

	return cmd
}

// RunFoo executes the business logic for the "foo" command.
func RunFoo(ctx context.Context, opt FooOptions) error {
	if opt.Bar == "" {
		return fmt.Errorf("--bar is required")
	}

	// Implementation logic here...
	fmt.Printf("Running foo with bar=%s\n", opt.Bar)
	return nil
}
```

## Benefits

*   **Separation of Concerns**: The CLI code (`BuildFooCommand`) deals with flags and Cobra specifics. The logic code (`RunFoo`) deals with the domain objects.
*   **Testability**: You can write unit tests for `RunFoo` by simply creating a `FooOptions` struct and calling the function. You don't need to mock standard input/output or execute a full Cobra command chain.
*   **Clarity**: It makes it obvious what inputs the command requires by looking at the `FooOptions` struct.
