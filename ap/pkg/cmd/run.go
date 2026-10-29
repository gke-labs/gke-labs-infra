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
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool"
	"github.com/spf13/cobra"
)

// BuildRunCommand constructs the cobra command for "run".
func BuildRunCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                "run <interpreter> <script> [args...]",
		Short:              "Run a script from the repository root with managed tools on PATH",
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
			if len(args) < 2 {
				return fmt.Errorf("requires at least 2 arguments: <interpreter> <script> [args...]")
			}
			return RunScript(cmd.Context(), args[0], args[1], args[2:])
		},
	}
	return cmd
}

// RunScript resolves repository root from script, prepares the tool environment, changes directory to repo root,
// and executes the interpreter with script and args.
func RunScript(ctx context.Context, interpreter, script string, scriptArgs []string) error {
	absScript, err := filepath.Abs(script)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for script %q: %w", script, err)
	}
	eval, err := filepath.EvalSymlinks(absScript)
	if err != nil {
		return fmt.Errorf("failed to resolve symlink for script %q: %w", script, err)
	}
	absScript = eval

	repoRoot, err := tool.FindRepoRootFromPath(absScript)
	if err != nil {
		return err
	}

	env, err := tool.ScriptEnv(repoRoot)
	if err != nil {
		return err
	}

	// Update PATH in current process so LookPath searches the newly prepared tool bin directory as well.
	for _, e := range env {
		if strings.HasPrefix(strings.ToUpper(e), "PATH=") {
			if err := os.Setenv("PATH", e[5:]); err != nil {
				return fmt.Errorf("failed to set PATH: %w", err)
			}
			break
		}
	}

	binary, err := exec.LookPath(interpreter)
	if err != nil {
		return fmt.Errorf("failed to find interpreter %q on PATH: %w", interpreter, err)
	}

	if err := os.Chdir(repoRoot); err != nil {
		return fmt.Errorf("failed to change directory to repo root %s: %w", repoRoot, err)
	}

	inv := &tool.Invocation{
		Binary: binary,
		Args:   append([]string{absScript}, scriptArgs...),
		Env:    env,
	}

	return tool.ExecProcess(inv)
}
