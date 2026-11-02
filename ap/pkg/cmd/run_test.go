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
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool"
)

func TestMain(m *testing.M) {
	if t, ok := tool.Resolve(os.Args[0]); ok {
		rootCmd := BuildRootCommand()
		repoRoot := tool.FindRepoRoot()
		if err := t.Exec(rootCmd.Context(), repoRoot, os.Args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "ap: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if os.Getenv("AP_TEST_EXEC") == "1" {
		rootCmd := BuildRootCommand()
		if err := rootCmd.Execute(); err != nil {
			fmt.Fprintf(os.Stderr, "ap: %v\n", err)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

func filterEnv(stripKeys ...string) []string {
	var env []string
outer:
	for _, e := range os.Environ() {
		for _, k := range stripKeys {
			if strings.HasPrefix(strings.ToUpper(e), strings.ToUpper(k)+"=") {
				continue outer
			}
		}
		env = append(env, e)
	}
	return env
}

func TestRun_Success(t *testing.T) {
	testExe, err := os.Executable()
	if err != nil {
		t.Fatalf("failed to get test executable: %v", err)
	}

	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0755); err != nil {
		t.Fatalf("failed to create .git: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, ".ap"), 0755); err != nil {
		t.Fatalf("failed to create .ap: %v", err)
	}

	taskDir := filepath.Join(repoDir, "dev", "tasks")
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		t.Fatalf("failed to create dev/tasks: %v", err)
	}

	scriptPath := filepath.Join(taskDir, "hello")
	scriptContent := "#!/bin/bash\n" +
		"set -e\n" +
		"echo \"PWD=$(pwd)\"\n" +
		"echo \"REPO_ROOT=$REPO_ROOT\"\n" +
		"echo \"KUBECTL=$(command -v kubectl)\"\n" +
		"echo \"ARGS=$*\"\n"
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	realRepoDir, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		realRepoDir = repoDir
	}
	expectedKubectl := filepath.Join(realRepoDir, ".build", "bin", "kubectl")

	baseEnv := filterEnv("REPO_ROOT", "AP_ROOT")
	baseEnv = append(baseEnv, "AP_TEST_EXEC=1", "AP_EXECUTABLE="+testExe)

	// Subtest 1: Invoked from subdirectory with relative script path
	t.Run("from subdirectory with relative path", func(t *testing.T) {
		cmd := exec.Command(testExe, "run", "bash", "./hello", "baz")
		cmd.Dir = taskDir
		cmd.Env = baseEnv

		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("run failed: %v\nOutput: %s", err, string(out))
		}

		output := string(out)
		if !strings.Contains(output, "PWD="+realRepoDir) {
			t.Errorf("expected PWD=%s, got output:\n%s", realRepoDir, output)
		}
		if !strings.Contains(output, "REPO_ROOT="+realRepoDir) {
			t.Errorf("expected REPO_ROOT=%s, got output:\n%s", realRepoDir, output)
		}
		if !strings.Contains(output, "KUBECTL="+expectedKubectl) {
			t.Errorf("expected KUBECTL=%s, got output:\n%s", expectedKubectl, output)
		}
		if !strings.Contains(output, "ARGS=baz") {
			t.Errorf("expected ARGS=baz, got output:\n%s", output)
		}
	})

	// Subtest 2: Shebang execution via #!/usr/bin/env -S ap run bash
	if runtime.GOOS != "windows" {
		t.Run("shebang execution", func(t *testing.T) {
			binDir := t.TempDir()
			apLink := filepath.Join(binDir, "ap")
			if err := os.Symlink(testExe, apLink); err != nil {
				t.Fatalf("failed to create ap symlink: %v", err)
			}

			shebangScript := filepath.Join(taskDir, "shebang-hello")
			content := "#!/usr/bin/env -S ap run bash\n" +
				"echo \"PWD=$(pwd)\"\n" +
				"echo \"REPO_ROOT=$REPO_ROOT\"\n" +
				"echo \"KUBECTL=$(command -v kubectl)\"\n" +
				"echo \"ARGS=$*\"\n"
			if err := os.WriteFile(shebangScript, []byte(content), 0755); err != nil {
				t.Fatalf("failed to write shebang script: %v", err)
			}

			outsideDir := t.TempDir()
			cmd := exec.Command(shebangScript, "shebang_arg")
			cmd.Dir = outsideDir

			env := filterEnv("REPO_ROOT", "AP_ROOT", "PATH")
			env = append(env, "PATH="+binDir+":"+os.Getenv("PATH"))
			env = append(env, "AP_TEST_EXEC=1", "AP_EXECUTABLE="+testExe)
			cmd.Env = env

			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("shebang script execution failed: %v\nOutput: %s", err, string(out))
			}

			output := string(out)
			if !strings.Contains(output, "PWD="+realRepoDir) {
				t.Errorf("expected PWD=%s, got output:\n%s", realRepoDir, output)
			}
			if !strings.Contains(output, "REPO_ROOT="+realRepoDir) {
				t.Errorf("expected REPO_ROOT=%s, got output:\n%s", realRepoDir, output)
			}
			if !strings.Contains(output, "KUBECTL="+expectedKubectl) {
				t.Errorf("expected KUBECTL=%s, got output:\n%s", expectedKubectl, output)
			}
			if !strings.Contains(output, "ARGS=shebang_arg") {
				t.Errorf("expected ARGS=shebang_arg, got output:\n%s", output)
			}
		})
	}
}

func TestRun_ErrorCases(t *testing.T) {
	testExe, err := os.Executable()
	if err != nil {
		t.Fatalf("failed to get test executable: %v", err)
	}
	baseEnv := filterEnv("REPO_ROOT", "AP_ROOT")
	baseEnv = append(baseEnv, "AP_TEST_EXEC=1", "AP_EXECUTABLE="+testExe)

	t.Run("fewer than 2 arguments", func(t *testing.T) {
		cmd := exec.Command(testExe, "run", "bash")
		cmd.Env = baseEnv
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected error for missing script argument, but succeeded:\n%s", string(out))
		}
		if !strings.Contains(string(out), "requires at least 2 arguments") {
			t.Errorf("expected 'requires at least 2 arguments' error message, got:\n%s", string(out))
		}
	})

	t.Run("script not in git repo", func(t *testing.T) {
		nonRepoDir := t.TempDir()
		scriptPath := filepath.Join(nonRepoDir, "script.sh")
		if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho hi\n"), 0755); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(testExe, "run", "bash", scriptPath)
		cmd.Env = baseEnv
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected error for script outside git repo, but succeeded:\n%s", string(out))
		}
		if !strings.Contains(string(out), "could not find git repository root") {
			t.Errorf("expected 'could not find git repository root' error, got:\n%s", string(out))
		}
	})
}
