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

	"github.com/gke-labs/gke-labs-infra/ap/pkg/imageref"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/kindload"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"github.com/spf13/cobra"
)

// KindLoadOptions holds configuration for the "kind-load" command.
type KindLoadOptions struct {
	*RootOptions
	Cluster string
	Images  []string
}

// BuildKindLoadCommand constructs the cobra command for "kind-load".
func BuildKindLoadCommand(rootOpt *RootOptions) *cobra.Command {
	opt := KindLoadOptions{
		RootOptions: rootOpt,
	}

	cmd := &cobra.Command{
		Use:   "kind-load [flags] <image>...",
		Short: "Load docker images into a kind cluster",
		Long: `Load one or more docker images from the local Docker daemon into a kind cluster.

Uses the same image-loading implementation as "ap deploy", bypassing the upstream kind
issue with containerd image store on multi-platform images.

Images must already exist in the local Docker daemon (built or pulled).`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opt.Images = args
			return RunKindLoad(cmd.Context(), opt)
		},
	}

	cmd.Flags().StringVar(&opt.Cluster, "cluster", "", "Target cluster (kind://<name> or kubeconfig context)")

	return cmd
}

// BuildKindLoadPlan constructs the KindLoadTask for the given options.
func BuildKindLoadPlan(opt KindLoadOptions, currentContext string) (*kindload.KindLoadTask, error) {
	tc, err := ResolveTargetCluster(opt.Cluster, currentContext)
	if err != nil {
		return nil, err
	}
	if !tc.IsKind() {
		target := opt.Cluster
		if target == "" {
			target = currentContext
		}
		if target == "" {
			target = "current context"
		}
		return nil, fmt.Errorf("%s is not a kind cluster; pass --cluster kind://<name>", target)
	}

	var imageRefs []*imageref.ImageRef
	for _, img := range opt.Images {
		ref, err := imageref.ParseImageRef(img)
		if err != nil {
			return nil, fmt.Errorf("invalid image %q: %w", img, err)
		}
		imageRefs = append(imageRefs, ref)
	}

	return &kindload.KindLoadTask{
		ClusterName: tc.KindClusterName(),
		Images:      imageRefs,
	}, nil
}

// RunKindLoad executes the business logic for the "kind-load" command.
func RunKindLoad(ctx context.Context, opt KindLoadOptions) error {
	if err := requireRepoRoot(opt.RootOptions); err != nil {
		return err
	}

	r, err := opt.GetSourceTree()
	if err != nil {
		return err
	}

	scope := tasks.NewTaskContext(opt.RepoRoot, opt.RepoRoot, r)

	var currentContext string
	if opt.Cluster == "" {
		kubectlBin, err := scope.Tool(ctx, "kubectl")
		if err != nil {
			return fmt.Errorf("failed to ensure kubectl to resolve current context: %w", err)
		}
		currentContext = getCurrentContext(ctx, kubectlBin)
	}

	task, err := BuildKindLoadPlan(opt, currentContext)
	if err != nil {
		return err
	}

	return tasks.Run(ctx, scope, []tasks.Task{task}, tasks.RunOptions{DryRun: opt.DryRun})
}
