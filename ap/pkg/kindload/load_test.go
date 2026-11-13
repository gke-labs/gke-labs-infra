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

package kindload

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadImages_RequiresKindBin(t *testing.T) {
	err := LoadImages(t.Context(), "", "ap-cluster", []string{"test-image:latest"}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected error when kindBin is empty, got nil")
	}
	if !strings.Contains(err.Error(), "kindBin is required") {
		t.Fatalf("expected error to contain 'kindBin is required', got: %v", err)
	}

	err = LoadImages(t.Context(), "", "ap-cluster", nil, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected error when kindBin is empty and images is nil, got nil")
	}
}

func TestLoadImages_EmptyImages(t *testing.T) {
	err := LoadImages(t.Context(), "kind", "ap-cluster", nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("expected nil when images is empty, got: %v", err)
	}
	err = LoadImages(t.Context(), "kind", "ap-cluster", []string{}, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("expected nil when images is empty, got: %v", err)
	}
}

func TestLoadImages_CommandFails(t *testing.T) {
	tempDir := t.TempDir()
	fakeBin := filepath.Join(tempDir, "kind")
	script := "#!/bin/sh\nexit 1\n"
	if err := os.WriteFile(fakeBin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	err := LoadImages(t.Context(), fakeBin, "test-cluster", []string{"test-image:latest"}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("expected error when kind fails, got nil")
	}
	if !strings.Contains(err.Error(), "failed to load image(s) into kind cluster \"test-cluster\"") {
		t.Errorf("expected error to mention cluster, got: %v", err)
	}
}

func TestLoadImages_NoNodesFound(t *testing.T) {
	tempDir := t.TempDir()
	fakeBin := filepath.Join(tempDir, "kind")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}

	err := LoadImages(t.Context(), fakeBin, "test-cluster", []string{"test-image:latest"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no nodes found for cluster") {
		t.Fatalf("expected error mentioning no nodes found, got: %v", err)
	}
}

func TestLoadImages_Success(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("PATH", tempDir+":"+os.Getenv("PATH"))
	logFile := filepath.Join(tempDir, "docker.log")

	fakeKind := filepath.Join(tempDir, "kind")
	if err := os.WriteFile(fakeKind, []byte("#!/bin/sh\necho node-alpha\necho node-beta\n"), 0755); err != nil {
		t.Fatal(err)
	}

	fakeDocker := filepath.Join(tempDir, "docker")
	dockerScript := `#!/bin/sh
echo "$@" >> "` + logFile + `"
[ "$1" = "save" ] && touch "$3" && exit 0
[ "$1" = "exec" ] && exit 0
exit 1
`
	if err := os.WriteFile(fakeDocker, []byte(dockerScript), 0755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := LoadImages(t.Context(), fakeKind, "test-cluster", []string{"app:v1", "app:v2"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("expected LoadImages to succeed, got: %v", err)
	}

	logged, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read docker log: %v", err)
	}
	loggedLines := strings.Split(strings.TrimSpace(string(logged)), "\n")
	if len(loggedLines) != 3 {
		t.Fatalf("expected 3 docker commands logged, got %d:\n%s", len(loggedLines), string(logged))
	}
	if !strings.HasPrefix(loggedLines[0], "save -o ") || !strings.Contains(loggedLines[0], "app:v1 app:v2") {
		t.Errorf("expected save command with both images, got: %q", loggedLines[0])
	}
	if !strings.Contains(loggedLines[1], "exec --privileged -i node-alpha ctr --namespace=k8s.io images import --digests -") {
		t.Errorf("expected import into node-alpha, got: %q", loggedLines[1])
	}
	if !strings.Contains(loggedLines[2], "exec --privileged -i node-beta ctr --namespace=k8s.io images import --digests -") {
		t.Errorf("expected import into node-beta, got: %q", loggedLines[2])
	}
}
