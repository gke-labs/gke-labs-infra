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

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/imageref"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/kindload"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/kind"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/kubectl"
)

// Options specifies configuration for the test harness.
type Options struct {
	// ClusterName is the name of the kind cluster to use.
	// If empty, "ap-e2e" will be used.
	ClusterName string

	// SkipClusterCreation can be set to true if the cluster is already expected to exist.
	SkipClusterCreation bool

	// SkipCleanup can be set to true to avoid deleting resources after the test.
	SkipCleanup bool
}

// Harness provides helper methods for running e2e tests against a Kubernetes cluster.
type Harness struct {
	t           *testing.T
	clusterName string
	namespace   string
	skipCleanup bool
	kindBin     string
	repoRoot    string
	kubectlBin  string
	sourceTree  *sourcetree.SourceTree
}

// NewHarness creates a new e2e test harness.
func NewHarness(t *testing.T, opts ...Options) *Harness {
	opt := Options{}
	if len(opts) > 0 {
		opt = opts[0]
	}

	if opt.ClusterName == "" {
		opt.ClusterName = "ap-e2e"
	}

	h := &Harness{
		t:           t,
		clusterName: opt.ClusterName,
		skipCleanup: opt.SkipCleanup || os.Getenv("SKIP_CLEANUP") != "",
	}

	h.ensureKind()
	h.ensureKubectl()
	h.setupCluster(opt.SkipClusterCreation)
	h.setupNamespace()

	return h
}

// KindCommand returns the path to the kind executable used by the harness.
func (h *Harness) KindCommand() string {
	if h.kindBin == "" {
		h.ensureKind()
	}
	return h.kindBin
}

// KindBin returns the path to the kind executable used by the harness.
func (h *Harness) KindBin() string {
	return h.KindCommand()
}

// RepoRoot returns the repository root path.
func (h *Harness) RepoRoot() string {
	if h.repoRoot == "" {
		dir, err := os.Getwd()
		if err != nil {
			if h.t != nil {
				h.t.Fatalf("failed to get current working directory: %v", err)
			}
			return ""
		}
		h.repoRoot = findRepoRoot(dir)
	}
	return h.repoRoot
}

func (h *Harness) ensureKind() {
	h.t.Helper()
	repoRoot := h.RepoRoot()
	bin, err := kind.EnsureKindForRepo(h.t.Context(), repoRoot)
	if err != nil {
		h.t.Fatalf("failed to prepare kind tool: %v", err)
	}
	h.kindBin = bin
}

// KubectlCommand returns the path to the kubectl executable used by the harness.
func (h *Harness) KubectlCommand() string {
	if h.kubectlBin == "" {
		h.ensureKubectl()
	}
	return h.kubectlBin
}

// KubectlBin returns the path to the kubectl executable used by the harness.
func (h *Harness) KubectlBin() string {
	return h.KubectlCommand()
}

func (h *Harness) ensureKubectl() {
	h.t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		h.t.Fatalf("failed to get current working directory: %v", err)
	}
	repoRoot := findRepoRoot(dir)
	bin, err := kubectl.EnsureKubectlForRepo(h.t.Context(), repoRoot)
	if err != nil {
		h.t.Fatalf("failed to prepare kubectl tool: %v", err)
	}
	h.kubectlBin = bin
}

func (h *Harness) setupCluster(skipCreation bool) {
	h.t.Helper()

	if !skipCreation {
		clusters := h.runCmd(h.KindCommand(), "get", "clusters")
		exists := false
		for _, cluster := range strings.Split(clusters, "\n") {
			if strings.TrimSpace(cluster) == h.clusterName {
				exists = true
				break
			}
		}

		if !exists {
			warnInotifyLimits()
			h.t.Logf("Creating kind cluster %s", h.clusterName)
			h.runCmd(h.KindCommand(), "create", "cluster", "--name", h.clusterName)
		}
	}

	// Ensure we are using the correct context
	contextName := "kind-" + h.clusterName
	h.runCmd(h.KubectlCommand(), "config", "use-context", contextName)
}

func (h *Harness) setupNamespace() {
	h.t.Helper()
	// Create a unique namespace for this test
	h.namespace = fmt.Sprintf("test-%d", rand.Intn(1000000))
	h.t.Logf("Creating namespace %s", h.namespace)
	h.runCmd(h.KubectlCommand(), "create", "namespace", h.namespace)

	if !h.skipCleanup {
		h.t.Cleanup(func() {
			h.t.Logf("Deleting namespace %s", h.namespace)
			h.runCmd(h.KubectlCommand(), "delete", "namespace", h.namespace, "--ignore-not-found")
		})
	}
}

// Namespace returns the name of the per-test namespace.
func (h *Harness) Namespace() string {
	return h.namespace
}

// KubectlApplyContent applies the given YAML content to the cluster in the test namespace.
func (h *Harness) KubectlApplyContent(content string) {
	h.t.Helper()
	cmd := exec.Command(h.KubectlCommand(), "apply", "-n", h.namespace, "-f", "-")
	cmd.Stdin = strings.NewReader(content)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		h.t.Fatalf("kubectl apply failed: %v\nStdout: %s\nStderr: %s", err, stdout.String(), stderr.String())
	}
}

// KubectlApplyFile applies the YAML file at the given path to the cluster in the test namespace.
func (h *Harness) KubectlApplyFile(path string) {
	h.t.Helper()
	h.runCmd(h.KubectlCommand(), "apply", "-n", h.namespace, "-f", path)
}

// KubectlDelete deletes the resource in the test namespace.
func (h *Harness) KubectlDelete(resource string) {
	h.t.Helper()
	h.runCmd(h.KubectlCommand(), "delete", "-n", h.namespace, resource, "--ignore-not-found")
}

// Wait waits for a resource to meet a certain condition.
func (h *Harness) Wait(resource, condition string, timeout time.Duration) {
	h.t.Helper()
	h.runCmd(h.KubectlCommand(), "wait", "-n", h.namespace, "--for="+condition, "--timeout="+timeout.String(), resource)
}

// WaitForDeployment waits for a deployment to be available.
func (h *Harness) WaitForDeployment(name string, timeout time.Duration) {
	h.t.Helper()
	h.Wait("deployment/"+name, "condition=available", timeout)
}

// GetPodLogs returns the logs of a pod in the test namespace.
func (h *Harness) GetPodLogs(name string) string {
	h.t.Helper()
	return h.runCmd(h.KubectlCommand(), "logs", name, "-n", h.namespace)
}

// DockerBuild builds a docker image with the given tag.
func (h *Harness) DockerBuild(tag, dockerfile, context string) {
	h.t.Helper()
	h.t.Logf("Building docker image %s", tag)
	h.runCmd("docker", "build", "-t", tag, "-f", dockerfile, context)
}

// LoadImages loads docker images into the specified kind cluster using the given kind binary.
// It retries on failure with exponential backoff.
func LoadImages(ctx context.Context, kindBin, cluster string, images []string) error {
	return kindload.LoadImages(ctx, kindBin, cluster, images)
}

// KindLoad loads a docker image into the kind cluster.
func (h *Harness) KindLoad(tag string) {
	h.t.Helper()
	h.t.Logf("Loading image %s into kind cluster %s", tag, h.clusterName)
	ref, err := imageref.ParseImageRef(tag)
	if err != nil {
		h.t.Fatalf("failed to parse image tag %q: %v", tag, err)
	}
	task := &kindload.KindLoadTask{
		ClusterName: h.clusterName,
		Images:      []*imageref.ImageRef{ref},
	}
	if h.sourceTree == nil {
		tree, err := sourcetree.Open(h.RepoRoot())
		if err != nil {
			h.t.Fatalf("failed to open sourcetree: %v", err)
		}
		h.sourceTree = tree
	}
	scope := tasks.NewScope(h.RepoRoot(), h.RepoRoot(), h.sourceTree)
	scope.KubectlBin = h.kubectlBin
	if err := tasks.RunTask(h.t.Context(), scope, task); err != nil {
		h.t.Fatalf("KindLoad failed: %v", err)
	}
}

// SetupRegistry deploys an in-cluster registry and waits for it to be ready.
func (h *Harness) SetupRegistry() {
	h.t.Helper()
	h.t.Log("Setting up in-cluster registry")
	cmd := exec.Command(h.KubectlCommand(), "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(RegistryManifests)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		h.t.Fatalf("kubectl apply failed: %v\nStdout: %s\nStderr: %s", err, stdout.String(), stderr.String())
	}
	h.runCmd(h.KubectlCommand(), "wait", "-n", "in-cluster-image-registry-system", "--for=jsonpath={.status.readyReplicas}=1", "--timeout=2m", "statefulset/in-cluster-image-registry")
}

// RunCmd runs a command and returns its output.
func (h *Harness) RunCmd(name string, args ...string) string {
	return h.runCmd(name, args...)
}

func findRepoRoot(startDir string) string {
	dir := startDir
	for {
		hasGit, err := fileutils.FileExists(filepath.Join(dir, ".git"))
		if err == nil && hasGit {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// FindRepoRoot finds the root of the git repository.
func (h *Harness) FindRepoRoot() string {
	h.t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		h.t.Fatalf("failed to get current working directory: %v", err)
	}
	root := findRepoRoot(dir)
	if root == "" {
		h.t.Fatalf("failed to find repo root")
	}
	return root
}

func (h *Harness) runCmd(name string, args ...string) string {
	h.t.Helper()
	h.t.Logf("Running command: %s %v", name, args)
	cmd := exec.Command(name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		h.t.Fatalf("Command %s %v failed: %v\nStdout: %s\nStderr: %s", name, args, err, stdout.String(), stderr.String())
	}
	return stdout.String()
}
