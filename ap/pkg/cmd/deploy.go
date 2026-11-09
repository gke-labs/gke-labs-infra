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
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/imageref"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/images"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/k8s"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/kindload"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"github.com/spf13/cobra"
)

// DeployOptions holds the configuration for the "deploy" command.
type DeployOptions struct {
	*RootOptions
	SkipPush     bool
	BuildkitHost string
	Namespace    string
	Cluster      string
	Targets      []string

	TargetCluster TargetCluster
	kindCluster   string

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
		Long: `Deploy artifacts by building container images and applying Kubernetes manifests.

Positional path arguments select either a directory to apply all manifests found under any k8s/ directory beneath it, or a single manifest file to apply only that file and build only its referenced images.

Flags configure the target cluster (--cluster), target namespace (-n, --namespace), remote buildkit daemon (--buildkit-host), and whether to skip pushing images (--skip-push).`,
		Args: cobra.ArbitraryArgs,
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
	cmd.Flags().StringVar(&opt.Cluster, "cluster", "", "Target cluster (kind://<name> or kubeconfig context)")

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

	if opt.Cluster != "" {
		tc, err := ResolveTargetCluster(opt.Cluster, "")
		if err != nil {
			return err
		}
		opt.TargetCluster = tc
	}

	r, err := opt.GetSourceTree()
	if err != nil {
		return err
	}

	currentDir := opt.Dir
	if currentDir == "" {
		currentDir = opt.RepoRoot
	}

	plans, err := buildDeployPlans(r, opt.RepoRoot, currentDir, args)
	if err != nil {
		return err
	}
	opt.plans = plans
	return nil
}

type deployPlan struct {
	dir        string
	manifests  []string
	imageNames []string
}

type targetFilter struct {
	arg  string
	path string
}

func resolveDeployTargets(r *sourcetree.SourceTree, repoRoot, currentDir string, targets []string) (string, bool, []targetFilter, error) {
	if len(targets) == 0 {
		targets = []string{"."}
	}

	whole := false
	var filters []targetFilter
	baseDir := currentDir
	if baseDir == "" {
		baseDir = repoRoot
	}

	for _, arg := range targets {
		switch arg {
		case ".":
			whole = true
		case "./...":
			whole = true
		case "//...":
			baseDir = repoRoot
			whole = true
		default:
			absPath := config.ResolveTargetPath(repoRoot, currentDir, arg)
			if !r.FileExists(absPath) && !r.DirExists(absPath) && absPath != repoRoot {
				return "", false, nil, fmt.Errorf("target path %q does not exist", arg)
			}
			rel, err := filepath.Rel(repoRoot, absPath)
			if err != nil || strings.HasPrefix(rel, "..") {
				return "", false, nil, fmt.Errorf("target path %q is not inside repository", arg)
			}
			if absPath == baseDir {
				whole = true
			} else {
				filters = append(filters, targetFilter{arg: arg, path: absPath})
			}
		}
	}

	return baseDir, whole, filters, nil
}

func selectManifests(r *sourcetree.SourceTree, dir string, whole bool, filters []targetFilter) ([]string, error) {
	allManifests := r.Under(dir).FindManifests()
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

func referencedImages(r *sourcetree.SourceTree, repoRoot string, manifests []string) ([]string, error) {
	availableImages, err := images.FindImageNames(r, repoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to find available images: %w", err)
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
				return nil, fmt.Errorf("manifest %s references placeholder image %q, but no images/%s/Dockerfile was found in %s", relManifest, img, img, repoRoot)
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

func buildDeployPlans(r *sourcetree.SourceTree, repoRoot, currentDir string, targets []string) ([]deployPlan, error) {
	baseDir, whole, filters, err := resolveDeployTargets(r, repoRoot, currentDir, targets)
	if err != nil {
		return nil, err
	}
	if baseDir == "" {
		return nil, fmt.Errorf("no scopes found for deploy")
	}

	manifests, err := selectManifests(r, baseDir, whole, filters)
	if err != nil {
		return nil, err
	}

	imageNames, err := referencedImages(r, repoRoot, manifests)
	if err != nil {
		return nil, err
	}

	if len(manifests) == 0 {
		return nil, nil
	}

	return []deployPlan{{
		dir:        baseDir,
		manifests:  manifests,
		imageNames: imageNames,
	}}, nil
}

// TargetCluster represents the resolved target Kubernetes cluster.
type TargetCluster struct {
	KubeContext string
	KindCluster string
}

// ParseKindClusterFromContext extracts the kind cluster name if the context represents a kind cluster.
// For example, "kind-foo" -> ("foo", true).
func ParseKindClusterFromContext(contextName string) (string, bool) {
	if cluster, ok := strings.CutPrefix(strings.TrimSpace(contextName), "kind-"); ok && cluster != "" {
		return cluster, true
	}
	return "", false
}

// ParseClusterFlag parses the --cluster flag into a kubeconfig context name.
// Accepted forms:
// - empty: unspecified (uses current context)
// - kind://<name>: target kind cluster <name> (context "kind-<name>")
// - <context>: target kubeconfig context
// Anything with an unknown scheme is an error naming the two accepted forms.
func ParseClusterFlag(val string) (string, error) {
	val = strings.TrimSpace(val)
	if val == "" {
		return "", nil
	}
	if strings.Contains(val, "://") {
		if name, ok := strings.CutPrefix(val, "kind://"); ok && name != "" {
			return "kind-" + name, nil
		}
		return "", fmt.Errorf("invalid --cluster %q; expected kind://<name> or <context>", val)
	}
	return val, nil
}

// ResolveTargetCluster resolves the cluster flag and context into a TargetCluster.
func ResolveTargetCluster(clusterFlag string, currentContext string) (TargetCluster, error) {
	if clusterFlag != "" {
		kCtx, err := ParseClusterFlag(clusterFlag)
		if err != nil {
			return TargetCluster{}, err
		}
		cluster, _ := ParseKindClusterFromContext(kCtx)
		return TargetCluster{
			KubeContext: kCtx,
			KindCluster: cluster,
		}, nil
	}
	cluster, _ := ParseKindClusterFromContext(currentContext)
	return TargetCluster{
		KindCluster: cluster,
	}, nil
}

var execCommandContext = exec.CommandContext

func getCurrentKubeContext(ctx context.Context, kubectlBin string) string {
	cmd := execCommandContext(ctx, kubectlBin, "config", "current-context")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// KindLoadTask is an alias to kindload.KindLoadTask.
type KindLoadTask = kindload.KindLoadTask

// BuildDeployPlan constructs the task tree for deploying artifacts according to DeployOptions.
func BuildDeployPlan(opt DeployOptions, r *sourcetree.SourceTree, buildkitHost string, naming images.ImageNaming) ([]tasks.Task, error) {
	tc := opt.TargetCluster
	if opt.Cluster != "" && tc.KubeContext == "" {
		resolved, err := ResolveTargetCluster(opt.Cluster, "")
		if err != nil {
			return nil, err
		}
		tc = resolved
	}
	if opt.kindCluster != "" && tc.KindCluster == "" {
		tc.KindCluster = opt.kindCluster
	}
	if tc.KindCluster != "" {
		naming.KindMode = true
	}
	push := !opt.SkipPush && tc.KindCluster == ""
	return buildDeployTasks(r, opt.plans, push, buildkitHost, opt.Namespace, tc, naming)
}

func buildDeployTasks(r *sourcetree.SourceTree, plans []deployPlan, push bool, buildkitHost string, namespace string, targetCluster TargetCluster, naming images.ImageNaming) ([]tasks.Task, error) {
	repoRoot := r.RepoRoot()
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
			Name: fmt.Sprintf("deploy-%s", filepath.Base(plan.dir)),
		}

		buildTasks, err := images.BuildTasksForImages(r, repoRoot, push, buildkitHost, plan.imageNames, naming)
		if err != nil {
			return nil, fmt.Errorf("build failed during deploy for %s: %w", plan.dir, err)
		}
		group.Tasks = append(group.Tasks, buildTasks)

		if targetCluster.KindCluster != "" && len(plan.imageNames) > 0 {
			var imageRefs []*imageref.ImageRef
			for _, img := range plan.imageNames {
				imageRefs = append(imageRefs, naming.Ref(img))
			}
			group.Tasks = append(group.Tasks, &KindLoadTask{
				ClusterName: targetCluster.KindCluster,
				Images:      imageRefs,
			})
		}

		deployTasks := k8s.DeployTasksForManifests(plan.manifests, k8s.DeployTaskOptions{
			Namespace:         namespace,
			OriginalNamespace: originalNamespace,
			Naming:            naming,
			KubeContext:       targetCluster.KubeContext,
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

	r, err := opt.GetSourceTree()
	if err != nil {
		return err
	}

	scope := tasks.NewTaskContext(opt.RepoRoot, opt.RepoRoot, r)
	kubectlBin, err := scope.Tool(ctx, "kubectl")
	if err != nil {
		return fmt.Errorf("failed to ensure kubectl: %w", err)
	}

	if opt.Cluster != "" && opt.TargetCluster.KubeContext == "" {
		tc, err := ResolveTargetCluster(opt.Cluster, "")
		if err != nil {
			return err
		}
		opt.TargetCluster = tc
	} else if opt.Cluster == "" && opt.TargetCluster.KindCluster == "" && opt.kindCluster == "" {
		tc, err := ResolveTargetCluster("", getCurrentKubeContext(ctx, kubectlBin))
		if err != nil {
			return err
		}
		opt.TargetCluster = tc
	}
	if opt.kindCluster != "" && opt.TargetCluster.KindCluster == "" {
		opt.TargetCluster.KindCluster = opt.kindCluster
	}

	if !opt.DryRun && opt.Namespace != "" {
		if err := k8s.DefaultCheckNamespaceCached(ctx, opt.Namespace, opt.TargetCluster.KubeContext, kubectlBin); err != nil {
			return err
		}
	}

	buildkitHost, err := images.ResolveBuildkitHost(opt.BuildkitHost)
	if err != nil {
		return err
	}

	cfg, err := config.Load(opt.RepoRoot)
	if err != nil {
		return err
	}
	naming := images.ResolveImageNaming(cfg)

	allTasks, err := BuildDeployPlan(opt, r, buildkitHost, naming)
	if err != nil {
		return err
	}

	return tasks.Run(ctx, scope, allTasks, tasks.RunOptions{DryRun: opt.DryRun})
}
