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

package fileutils

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFileExists(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Existing directory
	exists, err := FileExists(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error for existing directory: %v", err)
	}
	if !exists {
		t.Errorf("expected true for existing directory, got false")
	}

	// 2. Existing file
	filePath := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	exists, err = FileExists(filePath)
	if err != nil {
		t.Fatalf("unexpected error for existing file: %v", err)
	}
	if !exists {
		t.Errorf("expected true for existing file, got false")
	}

	// 3. Non-existent path
	missingPath := filepath.Join(tmpDir, "nonexistent.txt")
	exists, err = FileExists(missingPath)
	if err != nil {
		t.Fatalf("unexpected error for non-existent path: %v", err)
	}
	if exists {
		t.Errorf("expected false for non-existent path, got true")
	}

	// 4. Permission error (when running as non-root)
	if os.Geteuid() != 0 {
		restrictedDir := filepath.Join(tmpDir, "restricted")
		if err := os.Mkdir(restrictedDir, 0755); err != nil {
			t.Fatalf("failed to create dir: %v", err)
		}
		if err := os.Chmod(restrictedDir, 0000); err != nil {
			t.Fatalf("failed to chmod dir: %v", err)
		}
		defer os.Chmod(restrictedDir, 0755)

		inaccessiblePath := filepath.Join(restrictedDir, "child.txt")
		exists, err = FileExists(inaccessiblePath)
		if err == nil {
			t.Errorf("expected error for inaccessible path, got nil (exists=%v)", exists)
		} else if errors.Is(err, fs.ErrNotExist) {
			t.Errorf("expected permission error, but error matched fs.ErrNotExist: %v", err)
		}
	}
}

func TestTempFile(t *testing.T) {
	tempDir := t.TempDir()

	// 1. After Rename: destination exists with mode, temp file is gone, and Cleanup is a no-op.
	dest := filepath.Join(tempDir, "success.txt")
	tempFile, err := CreateTempFile(dest)
	if err != nil {
		t.Fatalf("CreateTempFile failed: %v", err)
	}
	defer tempFile.Cleanup()

	tempPath := findTempFile(t, tempDir, dest)
	if tempPath == "" {
		t.Fatalf("expected temporary file in %s for %s", tempDir, dest)
	}

	data := []byte("temp file content")
	if _, err := tempFile.Write(data); err != nil {
		t.Fatalf("failed to write to temp file: %v", err)
	}

	if err := tempFile.Rename(0755); err != nil {
		t.Fatalf("Rename failed: %v", err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("expected destination %s to exist: %v", dest, err)
	}
	if info.Mode().Perm() != 0755 {
		t.Errorf("got perm %v, want %v", info.Mode().Perm(), os.FileMode(0755))
	}
	gotData, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("failed reading destination: %v", err)
	}
	if string(gotData) != string(data) {
		t.Errorf("got content %q, want %q", string(gotData), string(data))
	}

	if _, err := os.Stat(tempPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected temp file %s to be gone after rename, got err: %v", tempPath, err)
	}

	// Cleanup is a no-op after Rename
	tempFile.Cleanup()
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("destination %s should still exist after Cleanup: %v", dest, err)
	}

	// 2. Without Rename: Cleanup removes temp file and destination does not exist.
	dest2 := filepath.Join(tempDir, "aborted.txt")
	tempFile2, err := CreateTempFile(dest2)
	if err != nil {
		t.Fatalf("CreateTempFile failed: %v", err)
	}
	tempPath2 := findTempFile(t, tempDir, dest2)
	if tempPath2 == "" {
		t.Fatalf("expected temporary file in %s for %s", tempDir, dest2)
	}

	if _, err := tempFile2.Write([]byte("discarded content")); err != nil {
		t.Fatalf("failed to write to temp file: %v", err)
	}

	tempFile2.Cleanup()

	if _, err := os.Stat(tempPath2); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected temp file %s to be removed by Cleanup, got err: %v", tempPath2, err)
	}
	if _, err := os.Stat(dest2); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected destination %s to not exist, got err: %v", dest2, err)
	}
}

func findTempFile(t *testing.T, dir, dest string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("failed to read dir: %v", err)
	}
	prefix := fmt.Sprintf(".%s-", filepath.Base(dest))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".tmp") {
			return filepath.Join(dir, name)
		}
	}
	return ""
}

func TestWriteFileAtomic(t *testing.T) {
	tmpDir := t.TempDir()
	targetPath := filepath.Join(tmpDir, "file.txt")

	// 1. Initial write: creates file, sets mode, leaves no temp files.
	data1 := []byte("first content")
	if err := WriteFileAtomic(targetPath, data1, 0640); err != nil {
		t.Fatalf("WriteFileAtomic failed: %v", err)
	}

	gotData, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if string(gotData) != string(data1) {
		t.Errorf("got %q, want %q", string(gotData), string(data1))
	}

	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("failed to stat file: %v", err)
	}
	if info.Mode().Perm() != 0640 {
		t.Errorf("got perm %v, want %v", info.Mode().Perm(), os.FileMode(0640))
	}
	assertNoTempFiles(t, tmpDir)

	// 2. Target replaced: replaces existing file, preserves new mode, leaves no temp files.
	data2 := []byte("second replaced content")
	if err := WriteFileAtomic(targetPath, data2, 0755); err != nil {
		t.Fatalf("WriteFileAtomic replace failed: %v", err)
	}

	gotData, err = os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("failed to read replaced file: %v", err)
	}
	if string(gotData) != string(data2) {
		t.Errorf("got %q, want %q", string(gotData), string(data2))
	}

	info, err = os.Stat(targetPath)
	if err != nil {
		t.Fatalf("failed to stat file: %v", err)
	}
	if info.Mode().Perm() != 0755 {
		t.Errorf("got perm %v, want %v", info.Mode().Perm(), os.FileMode(0755))
	}
	assertNoTempFiles(t, tmpDir)

	// 3. Failed write: renaming over a directory fails; verify no temp files left.
	subDir := filepath.Join(tmpDir, "subDirTarget")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(subDir, []byte("data"), 0644); err == nil {
		t.Fatalf("expected error when writing over a directory, got nil")
	}
	assertNoTempFiles(t, tmpDir)
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("failed to read dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".tmp") {
			t.Errorf("unexpected temp file found: %s", name)
		}
	}
}

func TestWriteFileAtomic_RunningScriptSelfRewrite(t *testing.T) {
	if os.Getenv("TEST_HELPER_REWRITE") == "1" {
		target := os.Getenv("TARGET_PATH")
		newContent := []byte(os.Getenv("NEW_CONTENT"))
		if err := WriteFileAtomic(target, newContent, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "WriteFileAtomic helper failed: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}

	testBin, err := os.Executable()
	if err != nil {
		t.Fatalf("failed to determine test executable: %v", err)
	}

	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "self_rewriter.sh")
	newContent := "#!/bin/bash\n# brand new generated script content\necho 'new output'\nexit 0\n"

	// Initial script that runs the helper to rewrite itself, followed by commands to verify
	// bash continues execution without syntax or offset errors.
	initialScript := fmt.Sprintf(`#!/bin/bash
set -o errexit
set -o nounset
set -o pipefail

# Invoke the helper to rewrite this script in place via WriteFileAtomic
"%s" -test.run=^TestWriteFileAtomic_RunningScriptSelfRewrite$

# Ensure bash reads and executes subsequent lines
echo "completed after self-rewrite"
exit 0
`, testBin)

	if err := os.WriteFile(scriptPath, []byte(initialScript), 0755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(t.Context(), bashPath, scriptPath)
	cmd.Env = append(os.Environ(),
		"TARGET_PATH="+scriptPath,
		"NEW_CONTENT="+newContent,
		"TEST_HELPER_REWRITE=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script execution failed: %v\nOutput: %s", err, string(out))
	}

	if !strings.Contains(string(out), "completed after self-rewrite") {
		t.Errorf("script output missing expected completion text; got:\n%s", string(out))
	}

	// Verify the script on disk now has the new content
	gotContent, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("failed to read rewritten script: %v", err)
	}
	if string(gotContent) != newContent {
		t.Errorf("script content mismatch after rewrite:\ngot:  %q\nwant: %q", string(gotContent), newContent)
	}
}

func TestPrependPath(t *testing.T) {
	// Empty dir returns unchanged env
	env := []string{"FOO=BAR"}
	if got := PrependPath(env, ""); len(got) != 1 || got[0] != "FOO=BAR" {
		t.Errorf("expected unchanged env, got %v", got)
	}

	// PATH not previously set
	got := PrependPath(env, "/custom/bin")
	found := false
	for _, e := range got {
		if strings.HasPrefix(e, "PATH=") {
			found = true
			if e != "PATH=/custom/bin" {
				t.Errorf("expected PATH=/custom/bin, got %s", e)
			}
		}
	}
	if !found {
		t.Error("expected PATH to be added")
	}

	// Existing PATH
	envWithExisting := []string{"FOO=BAR", "PATH=/usr/bin:/bin"}
	gotExisting := PrependPath(envWithExisting, "/custom/bin")
	foundExisting := false
	for _, e := range gotExisting {
		if strings.HasPrefix(e, "PATH=") {
			foundExisting = true
			expected := fmt.Sprintf("PATH=/custom/bin%c/usr/bin:/bin", os.PathListSeparator)
			if e != expected {
				t.Errorf("expected %s, got %s", expected, e)
			}
		}
	}
	if !foundExisting {
		t.Error("expected PATH to be updated")
	}
}

func TestIsExecutable(t *testing.T) {
	tempDir := t.TempDir()

	// Nonexistent file
	if IsExecutable(filepath.Join(tempDir, "nonexistent")) {
		t.Error("expected nonexistent file to return false")
	}

	// Directory
	if IsExecutable(tempDir) {
		t.Error("expected directory to return false")
	}

	// Regular non-executable file
	nonExec := filepath.Join(tempDir, "file.txt")
	if err := os.WriteFile(nonExec, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && IsExecutable(nonExec) {
		t.Error("expected 0644 file to return false")
	}

	// Executable file
	execFile := filepath.Join(tempDir, "script.sh")
	if err := os.WriteFile(execFile, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if !IsExecutable(execFile) {
		t.Error("expected 0755 file to return true")
	}
}
