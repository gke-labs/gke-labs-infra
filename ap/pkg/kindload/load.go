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
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	toolDocker "github.com/gke-labs/gke-labs-infra/ap/pkg/tool/docker"
)

// DefaultBackoffs defines the default retry backoffs for loading images into kind.
var DefaultBackoffs = []time.Duration{
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	32 * time.Second,
}

// KindLoadRetryPolicy defines the default retry policy used when retrying loading images into kind.
var KindLoadRetryPolicy tasks.RetryPolicy = tasks.Backoff(DefaultBackoffs)

// LoadImages loads docker images into the specified kind cluster using the given kind binary.
//
// We do not use `kind load docker-image` because of the upstream bug https://github.com/kubernetes-sigs/kind/issues/3795:
// when Docker uses the containerd image store (the default on Docker 29+, for example GitHub's ubuntu-26.04
// runners), a pulled image keeps the registry's multi-platform index but only the host platform's blobs.
// `docker save` writes the full index, and kind imports it with `ctr images import --all-platforms`, which fails
// with `content digest sha256:...: not found`. kind hardcodes `--all-platforms` (added in
// https://github.com/kubernetes-sigs/kind/pull/2957 so images for a non-host architecture are not filtered out);
// there is no flag to turn it off. The containerd side is https://github.com/containerd/containerd/issues/11344.
//
// kind's documented workaround is `docker image save --platform <platform>` plus `kind load image-archive`.
// We do not use it because `docker save --platform` is not available on the older Docker releases we still run
// on, and we want one loading path rather than a version switch. Trade-off we accept: without `--all-platforms`,
// only the node's platform is imported, so an image that exists locally only for a different architecture is not loaded.
//
// When to revisit: if kind fixes 3795 (for example https://github.com/kubernetes-sigs/kind/pull/4292 or a successor
// lands in a release we pin), go back to `kind load docker-image` and delete our import.
func LoadImages(ctx context.Context, kindBin, cluster string, images []string, stdout, stderr io.Writer) error {
	if kindBin == "" {
		return fmt.Errorf("kindBin is required")
	}
	if len(images) == 0 {
		return nil
	}

	env := fileutils.PrependPath(os.Environ(), filepath.Dir(kindBin))
	if dockerBin, err := toolDocker.EnsureDocker(ctx, ""); err == nil && dockerBin != "" {
		env = fileutils.PrependPath(env, filepath.Dir(dockerBin))
	}

	args := []string{"get", "nodes"}
	if cluster != "" {
		args = append(args, "--name", cluster)
	}

	var nodesStdout, nodesStderr bytes.Buffer
	nodesCmd := exec.CommandContext(ctx, kindBin, args...)
	nodesCmd.Stdout = &nodesStdout
	nodesCmd.Stderr = &nodesStderr
	nodesCmd.Env = env
	if err := nodesCmd.Run(); err != nil {
		if stderr != nil && nodesStderr.Len() > 0 {
			if _, writeErr := stderr.Write(nodesStderr.Bytes()); writeErr != nil {
				scoped.Log(ctx).Info("failed to write nodes stderr", "error", writeErr)
			}
		}
		return fmt.Errorf("failed to load image(s) into kind cluster %q: %w", cluster, err)
	}

	var nodes []string
	for _, line := range strings.Split(nodesStdout.String(), "\n") {
		node := strings.TrimSpace(line)
		if node != "" {
			nodes = append(nodes, node)
		}
	}
	if len(nodes) == 0 {
		return fmt.Errorf("failed to load image(s) into kind cluster %q: no nodes found for cluster", cluster)
	}

	tempDir, err := os.MkdirTemp("", "kindload-*")
	if err != nil {
		return fmt.Errorf("failed to load image(s) into kind cluster %q: %w", cluster, err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil && !os.IsNotExist(removeErr) {
			scoped.Log(ctx).Info("failed to remove temporary directory", "path", tempDir, "error", removeErr)
		}
	}()

	archivePath := filepath.Join(tempDir, "images.tar")
	saveArgs := append([]string{"save", "-o", archivePath}, images...)
	saveCmd := exec.CommandContext(ctx, "docker", saveArgs...)
	saveCmd.Env = env
	saveCmd.Stdout = stdout
	saveCmd.Stderr = stderr
	if err := saveCmd.Run(); err != nil {
		return fmt.Errorf("failed to load image(s) into kind cluster %q: %w", cluster, err)
	}

	for _, node := range nodes {
		if err := importImageArchive(ctx, node, archivePath, env, stdout, stderr); err != nil {
			return fmt.Errorf("failed to load image(s) into kind cluster %q: %w", cluster, err)
		}
	}
	return nil
}

// importImageArchive's ctr invocation mirrors kind's nodeutils.LoadImageArchive minus --all-platforms.
func importImageArchive(ctx context.Context, node, archivePath string, env []string, stdout, stderr io.Writer) (err error) {
	archiveFile, openErr := os.Open(archivePath)
	if openErr != nil {
		return fmt.Errorf("failed to open image archive: %w", openErr)
	}
	defer func() {
		if closeErr := archiveFile.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("failed to close image archive: %w", closeErr)
		}
	}()

	importCmd := exec.CommandContext(ctx, "docker", "exec", "--privileged", "-i", node, "ctr", "--namespace=k8s.io", "images", "import", "--digests", "-")
	importCmd.Env = env
	importCmd.Stdin = archiveFile
	importCmd.Stdout = stdout
	importCmd.Stderr = stderr
	if runErr := importCmd.Run(); runErr != nil {
		return fmt.Errorf("failed to import image into node %s: %w", node, runErr)
	}
	return nil
}
