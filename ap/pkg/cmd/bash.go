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
	"strconv"
	"strings"
	"sync"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
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
			if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}
			if len(args) > 0 && args[0] == "--" {
				args = args[1:]
			}
			if len(args) < 1 {
				return fmt.Errorf("requires at least 1 argument: <script> [args...]")
			}
			return RunScript(cmd.Context(), args[0], args[1:])
		},
	}
	return cmd
}

// RunScript resolves repository root from script, prepares the tool environment, changes directory to repo root,
// and executes bash with script and args.
func RunScript(ctx context.Context, script string, scriptArgs []string) error {
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

	binary, err := exec.LookPath("bash")
	if err != nil {
		return fmt.Errorf("failed to find bash on PATH: %w", err)
	}

	checkBashVersion(binary)

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

var checkBashOnce sync.Once

// checkBashVersion verifies that the resolved bash binary is version 4 or newer.
// If it is older than 4, it emits a warning. The check runs once per process.
func checkBashVersion(binary string) {
	checkBashOnce.Do(func() {
		cmd := exec.Command(binary, "--version")
		out, err := cmd.Output()
		if err != nil {
			klog.V(2).Infof("failed to run %s --version: %v", binary, err)
			return
		}
		ver, major, err := parseBashVersion(string(out))
		if err != nil {
			klog.V(2).Infof("failed to parse bash version from %s: %v", binary, err)
			return
		}
		if major < 4 {
			klog.Warningf("bash at %s is %s; scripts run in CI with bash 5. Install a newer bash (e.g. brew install bash) and make sure it is first on PATH.", binary, ver)
		}
	})
}

// parseBashVersion extracts the version string and major version number from
// the output (or first line) of "bash --version".
func parseBashVersion(output string) (string, int, error) {
	lines := strings.Split(output, "\n")
	var firstLine string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			firstLine = l
			break
		}
	}
	if firstLine == "" {
		return "", 0, fmt.Errorf("empty bash version output")
	}

	fields := strings.Fields(firstLine)
	for i, f := range fields {
		if f == "version" && i+1 < len(fields) {
			version := fields[i+1]
			majorStr := strings.TrimPrefix(strings.Split(version, ".")[0], "v")
			major, err := strconv.Atoi(majorStr)
			if err != nil {
				return "", 0, fmt.Errorf("failed to parse major version from %q: %w", version, err)
			}
			return version, major, nil
		}
	}
	return "", 0, fmt.Errorf("could not find version token in bash output: %q", firstLine)
}
