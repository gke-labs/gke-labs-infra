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
	"sort"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/e2e"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/imageref"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/images"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/k8s"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/repo"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	toolDocker "github.com/gke-labs/gke-labs-infra/ap/pkg/tool/docker"
	toolKind "github.com/gke-labs/gke-labs-infra/ap/pkg/tool/kind"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

// DeployOptions holds the configuration for the "deploy" command.
type DeployOptions struct {
	*RootOptions
	SkipPush     bool
	BuildkitHost string
	Namespace    string
	Targets      []string

	kindCluster string

	plans []deployPlan
}

// BuildDeployCommand constructs the cobra command for "deploy".
func BuildDeployCommand(rootOpt *RootOptions) *cobra.Command {
	opt := DeployOptions{
		RootOptions: rootOpt,
	}

	cmd := &cobra.Command{
		Use:   "deploy [path...]",
		Short: "Deploy artifacts",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opt.Resolve(args); err != nil {
				return err
			}
			return RunDeploy(cmd.Context(), opt)
		},
	}

	cmd.Flags().BoolVar(&opt.SkipPush, "skip-push", false, "Skip pushing images to registry")
	cmd.Flags().StringVar(&opt.BuildkitHost, "buildkit-host", "", "Buildkit host to use (e.g. k8s://namespace/service)")
	cmd.Flags().StringVarP(&opt.Namespace, "namespace", "n", "", "Target namespace for namespaced manifests")

	return cmd
}

// Resolve resolves the target paths based on args for deploy.
func (opt *DeployOptions) Resolve(args []string) error {
	opt.Targets = args
	if opt.RepoRoot == "" {
		return fmt.Errorf("repo root is not set")
	}

	if err := k8s.ValidateNamespaceName(opt.Namespace); err != nil {
		return err
	}

	if opt.RootFlag != "" {
		if err := opt.ResolveRoot(); err != nil {
			return err
		}
	}

	r, err := opt.GetRepo()
	if err != nil {
		return err
	}

	currentAPRoot := opt.APRoot
	if currentAPRoot == "" && len(opt.APRoots) == 1 {
		currentAPRoot = opt.APRoots[0]
	}

	plans, err := buildDeployPlans(r, opt.RepoRoot, currentAPRoot, args)
	if err != nil {
		return err
	}
	opt.plans = plans

	var roots []string
	for _, p := range plans {
		roots = append(roots, p.apRoot)
	}
	opt.APRoots = roots
	return nil
}

type deployPlan struct {
	apRoot     string
	manifests  []string
	imageNames []string
}

type targetFilter struct {
	arg  string
	path string
}

func resolveDeployTargets(r *repo.Repo, repoRoot, currentAPRoot string, targets []string) ([]string, map[string]bool, map[string][]targetFilter, error) {
	if len(targets) == 0 {
		targets = []string{"."}
	}

	wholeRoot := make(map[string]bool)
	filtersByRoot := make(map[string][]targetFilter)
	var roots []string
	seenRoot := make(map[string]bool)

	addRoot := func(root string) {
		if !seenRoot[root] {
			seenRoot[root] = true
			roots = append(roots, root)
		}
	}

	for _, arg := range targets {
		switch arg {
		case ".":
			root := currentAPRoot
			if root == "" {
				root = repoRoot
			}
			wholeRoot[root] = true
			addRoot(root)
		case "./...":
			base := currentAPRoot
			if base == "" {
				base = repoRoot
			}
			all, err := config.FindAllAPRoots(r, base)
			if err != nil {
				return nil, nil, nil, err
			}
			if len(all) == 0 {
				all = []string{base}
			}
			for _, root := range all {
				wholeRoot[root] = true
				addRoot(root)
			}
		case "//...":
			all, err := config.FindAllAPRoots(r, repoRoot)
			if err != nil {
				return nil, nil, nil, err
			}
			if len(all) == 0 {
				all = []string{repoRoot}
			}
			for _, root := range all {
				wholeRoot[root] = true
				addRoot(root)
			}
		default:
			absPath := config.ResolveTargetPath(repoRoot, currentAPRoot, arg)
			if !r.FileExists(absPath) && !r.DirExists(absPath) && absPath != repoRoot {
				return nil, nil, nil, fmt.Errorf("target path %q does not exist", arg)
			}
			apRoot := r.APRootFor(absPath)
			if apRoot == "" {
				return nil, nil, nil, fmt.Errorf("target path %q is not inside an AP root", arg)
			}
			addRoot(apRoot)
			if absPath == apRoot {
				wholeRoot[apRoot] = true
			} else {
				filtersByRoot[apRoot] = append(filtersByRoot[apRoot], targetFilter{arg: arg, path: absPath})
			}
		}
	}

	sort.Strings(roots)
	return roots, wholeRoot, filtersByRoot, nil
}

func selectManifests(r *repo.Repo, apRoot string, whole bool, filters []targetFilter) ([]string, error) {
	allManifests := r.Under(apRoot).FindManifests()
	if whole || len(filters) == 0 {
		return allManifests, nil
	}

	seenManifest := make(map[string]bool)
	var selectedManifests []string
	for _, f := range filters {
		matched := false
		for _, m := range allManifests {
			cleanM := filepath.Clean(m)
			if cleanM == f.path || strings.HasPrefix(cleanM, f.path+string(filepath.Separator)) {
				matched = true
				if !seenManifest[cleanM] {
					seenManifest[cleanM] = true
					selectedManifests = append(selectedManifests, cleanM)
				}
			}
		}
		if !matched {
			return nil, fmt.Errorf("no manifests found under %q", f.arg)
		}
	}
	sort.Strings(selectedManifests)
	return selectedManifests, nil
}

func referencedImages(r *repo.Repo, apRoot, repoRoot string, manifests []string) ([]string, error) {
	availableImages, err := images.FindImageNames(r, apRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to find available images in %s: %w", apRoot, err)
	}
	availableSet := make(map[string]bool, len(availableImages))
	for _, img := range availableImages {
		availableSet[img] = true
	}

	referencedSet := make(map[string]bool)
	for _, manifest := range manifests {
		imgs, err := k8s.FindReferencedImagesInFile(manifest)
		if err != nil {
			return nil, fmt.Errorf("failed to parse manifest %s: %w", manifest, err)
		}
		for _, img := range imgs {
			if !availableSet[img] {
				relManifest, err := filepath.Rel(repoRoot, manifest)
				if err != nil {
					relManifest = manifest
				}
				return nil, fmt.Errorf("manifest %s references placeholder image %q, but no images/%s/Dockerfile was found in %s", relManifest, img, img, apRoot)
			}
			referencedSet[img] = true
		}
	}

	var imageNames []string
	for img := range referencedSet {
		imageNames = append(imageNames, img)
	}
	sort.Strings(imageNames)
	return imageNames, nil
}

func buildDeployPlans(r *repo.Repo, repoRoot, currentAPRoot string, targets []string) ([]deployPlan, error) {
	roots, wholeRoot, filtersByRoot, err := resolveDeployTargets(r, repoRoot, currentAPRoot, targets)
	if err != nil {
		return nil, err
	}

	var plans []deployPlan
	for _, apRoot := range roots {
		manifests, err := selectManifests(r, apRoot, wholeRoot[apRoot], filtersByRoot[apRoot])
		if err != nil {
			return nil, err
		}

		imageNames, err := referencedImages(r, apRoot, repoRoot, manifests)
		if err != nil {
			return nil, err
		}

		plans = append(plans, deployPlan{
			apRoot:     apRoot,
			manifests:  manifests,
			imageNames: imageNames,
		})
	}
	return plans, nil
}

// ParseKindClusterFromContext extracts the kind cluster name if the context represents a kind cluster.
// For example, "kind-foo" -> ("foo", true).
func ParseKindClusterFromContext(contextName string) (string, bool) {
	if cluster, ok := strings.CutPrefix(strings.TrimSpace(contextName), "kind-"); ok && cluster != "" {
		return cluster, true
	}
	return "", false
}

var execCommandContext = exec.CommandContext

func getCurrentKubeContext(ctx context.Context) string {
	cmd := execCommandContext(ctx, "kubectl", "config", "current-context")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// KindLoadTask loads docker images into a kind cluster using the tool-managed kind binary.
type KindLoadTask struct {
	RepoRoot     string
	ClusterName  string
	Images       []string
	ImageDigests map[string]string
}

func (t *KindLoadTask) Run(ctx context.Context, scope *tasks.APScope) error {
	if len(t.Images) == 0 {
		return nil
	}
	root := t.RepoRoot
	if root == "" {
		root = scope.RepoRoot
	}
	kindBin, err := toolKind.EnsureKindForRepo(ctx, root)
	if err != nil {
		return fmt.Errorf("failed to ensure kind tool: %w", err)
	}
	if err := e2e.LoadImages(ctx, kindBin, t.ClusterName, t.Images); err != nil {
		return err
	}

	if len(t.ImageDigests) > 0 {
		t.tagContainerdDigests(ctx, kindBin, root)
	}

	return nil
}

// tagContainerdDigests tags loaded images in kind's containerd image store with their buildx
// manifest digests so they are addressable by digest (e.g. tag@sha256:<digest>).
//
// This is kind-specific: on standard registry push paths, the remote registry receives the OCI
// manifest index containing the canonical digest, and kubelet resolves/pulls the digest from the
// registry. However, when deploying locally to a kind cluster, `kind load docker-image` archives
// the local docker image via `docker save` and imports it into containerd via `ctr images import`.
// That archive import creates local repository tags (e.g. repo:latest) but does not record the
// buildx manifest digest in containerd's image index. Consequently, containerd treats a digest-pinned
// image reference in the pod spec (repo:latest@sha256:<digest>) as a local cache miss and attempts
// to pull it from the remote registry (which fails for never-pushed images). Explicitly adding the
// digest tag in containerd via `ctr --namespace=k8s.io images tag` ensures containerd recognizes
// the loaded image locally when kubelet resolves the digest.
func (t *KindLoadTask) tagContainerdDigests(ctx context.Context, kindBin, root string) {
	dockerBin, err := toolDocker.EnsureDocker(ctx, "")
	if err != nil || dockerBin == "" {
		dockerBin = "docker"
	}

	cmd := execCommandContext(ctx, kindBin, "get", "nodes", "--name", t.ClusterName)
	cmd.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(kindBin))
	if dockerBin != "docker" {
		cmd.Env = fileutils.PrependPath(cmd.Env, filepath.Dir(dockerBin))
	}
	out, err := cmd.Output()
	var nodes []string
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				nodes = append(nodes, line)
			}
		}
	}
	if len(nodes) == 0 {
		nodes = []string{t.ClusterName + "-control-plane"}
	}

	for _, fullImage := range t.Images {
		ref, err := imageref.Parse(fullImage)
		if err != nil {
			continue
		}
		imgName := ref.Name()
		digest := t.ImageDigests[imgName]
		if digest == "" || !strings.HasPrefix(digest, "sha256:") {
			continue
		}

		for _, node := range nodes {
			tagCmd1 := execCommandContext(ctx, dockerBin, "exec", node, "ctr", "--namespace=k8s.io", "images", "tag", fullImage, fullImage+"@"+digest)
			if err := tagCmd1.Run(); err != nil {
				klog.V(2).Infof("failed to tag %s in containerd on node %s: %v", fullImage, node, err)
			}

			refWithoutTag := ref
			refWithoutTag.Tag = ""
			tagCmd2 := execCommandContext(ctx, dockerBin, "exec", node, "ctr", "--namespace=k8s.io", "images", "tag", fullImage, refWithoutTag.String()+"@"+digest)
			if err := tagCmd2.Run(); err != nil {
				klog.V(2).Infof("failed to tag %s in containerd on node %s: %v", refWithoutTag.String(), node, err)
			}
		}
	}
}

func (t *KindLoadTask) GetName() string {
	if t.ClusterName != "" {
		return fmt.Sprintf("kind-load (cluster: %s)", t.ClusterName)
	}
	return "kind-load"
}

func (t *KindLoadTask) GetChildren() []tasks.Task {
	return nil
}

// BuildDeployPlan constructs the task tree for deploying artifacts according to DeployOptions.
func BuildDeployPlan(opt DeployOptions, r *repo.Repo, buildkitHost string) ([]tasks.Task, error) {
	push := !opt.SkipPush && opt.kindCluster == ""
	return buildDeployTasks(r, opt.plans, push, buildkitHost, opt.Namespace, opt.kindCluster)
}

func buildDeployTasks(r *repo.Repo, plans []deployPlan, push bool, buildkitHost string, namespace string, kindCluster string) ([]tasks.Task, error) {
	var allTasks []tasks.Task

	var originalNamespace string
	if namespace != "" {
		var allManifests []string
		for _, plan := range plans {
			allManifests = append(allManifests, plan.manifests...)
		}
		originalNamespace = k8s.FindOriginalNamespaceInFiles(allManifests, nil)
	}

	for _, plan := range plans {
		group := &tasks.Group{
			Name: fmt.Sprintf("deploy-%s", filepath.Base(plan.apRoot)),
		}

		cfg, err := config.Load(plan.apRoot)
		if err != nil {
			return nil, err
		}
		imageRepo := cfg.ImageRepo()
		tag := cfg.ImageTag()

		imageDigests := make(map[string]string)

		buildTasks, err := images.BuildTasksForImages(r.Under(plan.apRoot), plan.apRoot, push, buildkitHost, plan.imageNames, images.BuildTaskOptions{
			ImageDigests: imageDigests,
		})
		if err != nil {
			return nil, fmt.Errorf("build failed during deploy for %s: %w", plan.apRoot, err)
		}
		group.Tasks = append(group.Tasks, buildTasks)

		if kindCluster != "" && len(plan.imageNames) > 0 {
			var fullImages []string
			for _, img := range plan.imageNames {
				fullImages = append(fullImages, images.FullImageName(imageRepo, img, tag))
			}
			group.Tasks = append(group.Tasks, &KindLoadTask{
				RepoRoot:     plan.apRoot,
				ClusterName:  kindCluster,
				Images:       fullImages,
				ImageDigests: imageDigests,
			})
		}

		deployTasks := k8s.DeployTasksForManifests(plan.manifests, k8s.DeployTaskOptions{
			Namespace:         namespace,
			OriginalNamespace: originalNamespace,
			ImageRepository:   imageRepo,
			ImageTag:          tag,
			ImageDigests:      imageDigests,
		})
		group.Tasks = append(group.Tasks, deployTasks)

		allTasks = append(allTasks, group)
	}

	return allTasks, nil
}

// RunDeploy executes the business logic for the "deploy" command.
func RunDeploy(ctx context.Context, opt DeployOptions) error {
	if err := requireRepoRoot(opt.RootOptions); err != nil {
		return err
	}

	if !opt.DryRun && opt.Namespace != "" {
		if err := k8s.DefaultCheckNamespaceCached(ctx, opt.Namespace); err != nil {
			return err
		}
	}

	buildkitHost, err := images.ResolveBuildkitHost(opt.BuildkitHost)
	if err != nil {
		return err
	}

	r, err := opt.GetRepo()
	if err != nil {
		return err
	}

	cfg, err := config.Load(opt.RepoRoot)
	if err != nil {
		return err
	}
	if cfg.ImageRepo() == "" {
		return fmt.Errorf("IMAGE_PREFIX is not set; it is required for deploy")
	}

	if cfg.ImageRepo() != "images.local" {
		if cluster, ok := ParseKindClusterFromContext(getCurrentKubeContext(ctx)); ok {
			opt.kindCluster = cluster
		}
	}

	allTasks, err := BuildDeployPlan(opt, r, buildkitHost)
	if err != nil {
		return err
	}

	return tasks.Run(ctx, &tasks.APScope{RepoRoot: opt.RepoRoot, Dir: opt.RepoRoot, Repo: r}, allTasks, tasks.RunOptions{DryRun: opt.DryRun})
}
