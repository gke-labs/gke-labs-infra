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

package tool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const fakeSymlinkToolName = "fake-symlink-tool"

func init() {
	registry = append(registry, &Tool{
		Name: fakeSymlinkToolName,
		Prepare: func(ctx context.Context, repoRoot string, args []string) (*Invocation, error) {
			fakeBin := os.Getenv("AP_TEST_FAKE_TOOL_BIN")
			if fakeBin == "" {
				return nil, fmt.Errorf("AP_TEST_FAKE_TOOL_BIN not set")
			}
			return &Invocation{
				Binary: fakeBin,
				Args:   args,
			}, nil
		},
	})
}

func TestMain(m *testing.M) {
	if t, ok := Resolve(os.Args[0]); ok {
		ctx := context.Background()
		repoRoot := FindRepoRoot()
		if err := t.Exec(ctx, repoRoot, os.Args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "ap: %v\n", err)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

func TestResolveTool(t *testing.T) {
	tests := []struct {
		argv0       string
		wantName    string
		wantResolve bool
	}{
		// Registered tool names
		{argv0: "kind", wantName: "kind", wantResolve: true},
		{argv0: "kubectl", wantName: "kubectl", wantResolve: true},
		{argv0: "docker", wantName: "docker", wantResolve: true},
		{argv0: "docker-buildx", wantName: "docker-buildx", wantResolve: true},
		{argv0: "protoc", wantName: "protoc", wantResolve: true},
		{argv0: "go", wantName: "go", wantResolve: true},

		// Executable paths with directories
		{argv0: "/usr/local/bin/kind", wantName: "kind", wantResolve: true},
		{argv0: "/usr/local/bin/kubectl", wantName: "kubectl", wantResolve: true},
		{argv0: "/usr/bin/docker", wantName: "docker", wantResolve: true},
		{argv0: "/opt/bin/docker-buildx", wantName: "docker-buildx", wantResolve: true},
		{argv0: "./bin/protoc", wantName: "protoc", wantResolve: true},
		{argv0: "/usr/local/bin/go", wantName: "go", wantResolve: true},

		// Windows .exe extensions
		{argv0: "kind.exe", wantName: "kind", wantResolve: true},
		{argv0: "kubectl.exe", wantName: "kubectl", wantResolve: true},
		{argv0: "docker.exe", wantName: "docker", wantResolve: true},
		{argv0: "docker-buildx.exe", wantName: "docker-buildx", wantResolve: true},
		{argv0: "protoc.exe", wantName: "protoc", wantResolve: true},
		{argv0: "go.exe", wantName: "go", wantResolve: true},
		{argv0: "C:\\tools\\bin\\kind.exe", wantName: "kind", wantResolve: true},
		{argv0: "C:\\tools\\bin\\kubectl.exe", wantName: "kubectl", wantResolve: true},
		{argv0: "C:\\tools\\bin\\docker.exe", wantName: "docker", wantResolve: true},
		{argv0: "C:\\tools\\bin\\go.exe", wantName: "go", wantResolve: true},

		// Unknown / normal CLI names that fall through
		{argv0: "ap", wantResolve: false},
		{argv0: "ap.exe", wantResolve: false},
		{argv0: "/usr/local/bin/ap", wantResolve: false},
		{argv0: "other", wantResolve: false},
		{argv0: "", wantResolve: false},
	}

	for _, tc := range tests {
		t.Run(tc.argv0, func(t *testing.T) {
			gotTool, gotResolve := Resolve(tc.argv0)
			if gotResolve != tc.wantResolve {
				t.Fatalf("Resolve(%q) got resolve=%v, want %v", tc.argv0, gotResolve, tc.wantResolve)
			}
			if tc.wantResolve {
				if gotTool == nil {
					t.Fatalf("Resolve(%q) returned nil tool", tc.argv0)
				}
				if gotTool.Name != tc.wantName {
					t.Fatalf("Resolve(%q) tool name = %q, want %q", tc.argv0, gotTool.Name, tc.wantName)
				}
			} else {
				if gotTool != nil {
					t.Fatalf("Resolve(%q) expected nil tool, got %v", tc.argv0, gotTool)
				}
			}
		})
	}
}

func TestToolSymlink_PreservesWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinking is not supported on Windows")
	}

	// Create a fake tool binary (a script that prints pwd and its arguments).
	fakeBinDir := t.TempDir()
	fakeBin := filepath.Join(fakeBinDir, "fake-tool-bin.sh")
	script := "#!/bin/sh\npwd\nfor a in \"$@\"; do\n  echo \"arg: $a\"\ndone\n"
	if err := os.WriteFile(fakeBin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	// Create a repository structure with .ap and .git at the root.
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create a subdirectory inside the repository.
	subDir := filepath.Join(repoDir, "sub", "pkg")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a symlink named fakeSymlinkToolName pointing to the test executable.
	testExe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	symlinkDir := t.TempDir()
	symlinkPath := filepath.Join(symlinkDir, fakeSymlinkToolName)
	if err := os.Symlink(testExe, symlinkPath); err != nil {
		t.Fatal(err)
	}

	// Invoke the tool via its symlink from the subdirectory with a relative path argument.
	relArg := filepath.Join("relative", "file.proto")
	cmd := exec.Command("sh", "-c", "cd \"$1\" && exec \"$2\" \"$3\"", "sh", subDir, symlinkPath, relArg)

	// Strip AP_ROOT and REPO_ROOT from environment so FindRoots discovers the root by walking up from subDir.
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "AP_ROOT=") && !strings.HasPrefix(e, "REPO_ROOT=") {
			env = append(env, e)
		}
	}
	cmd.Env = append(env, "AP_TEST_FAKE_TOOL_BIN="+fakeBin)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("invoking tool via symlink failed: %v\nOutput: %s", err, string(out))
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		t.Fatalf("unexpected output format, got %d lines: %q", len(lines), string(out))
	}

	gotPwd := lines[0]
	// Resolve symlinks to handle OS temp dir symlinks (e.g. /tmp vs /private/tmp on macOS).
	canonicalSubDir, err := filepath.EvalSymlinks(subDir)
	if err != nil {
		t.Fatalf("failed to eval symlinks for subDir: %v", err)
	}
	canonicalGotPwd, err := filepath.EvalSymlinks(gotPwd)
	if err != nil {
		t.Fatalf("failed to eval symlinks for gotPwd: %v", err)
	}

	if canonicalGotPwd != canonicalSubDir {
		t.Errorf("working directory mismatch: got %q, want %q", canonicalGotPwd, canonicalSubDir)
	}
	if lines[1] != "arg: "+relArg {
		t.Errorf("argument mismatch: got %q, want %q", lines[1], "arg: "+relArg)
	}
}

func TestFindRepoRootFromPath(t *testing.T) {
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	nestedDir := filepath.Join(repoDir, "a", "b", "c")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(nestedDir, "script.sh")
	if err := os.WriteFile(filePath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Run("nested file", func(t *testing.T) {
		root, err := FindRepoRootFromPath(filePath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if root != repoDir {
			t.Errorf("expected root %s, got %s", repoDir, root)
		}
	})

	t.Run("outside git repository", func(t *testing.T) {
		nonRepoDir := t.TempDir()
		nonRepoFile := filepath.Join(nonRepoDir, "file.txt")
		if err := os.WriteFile(nonRepoFile, []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
		_, err := FindRepoRootFromPath(nonRepoFile)
		if err == nil {
			t.Fatal("expected error for path outside git repository")
		}
	})
}
