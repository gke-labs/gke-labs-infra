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
	"context"
	"fmt"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/imageref"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

// KindLoadTask loads docker images into a kind cluster using the tool-managed kind binary.
type KindLoadTask struct {
	// ClusterName is the name of the kind cluster where images will be loaded.
	ClusterName string

	// Images holds the image references to load. At plan time these are the
	// base references; after the build task runs they carry the unique tag or
	// digest produced by the build.
	Images []*imageref.ImageRef
}

func (t *KindLoadTask) Run(ctx context.Context, scope *tasks.TaskContext) error {
	if len(t.Images) == 0 {
		return nil
	}
	var imagesToLoad []string
	for _, ref := range t.Images {
		imagesToLoad = append(imagesToLoad, ref.String())
	}
	if scope == nil {
		return fmt.Errorf("task context is required")
	}
	kindBin, err := scope.Tool(ctx, "kind")
	if err != nil {
		return fmt.Errorf("failed to ensure kind tool: %w", err)
	}
	return LoadImages(ctx, kindBin, t.ClusterName, imagesToLoad, scope.Stdout(), scope.Stderr())
}

func (t *KindLoadTask) GetName() string {
	prefix := "kind-load"
	if t.ClusterName != "" {
		prefix = fmt.Sprintf("kind-load (cluster: %s)", t.ClusterName)
	}
	return fmt.Sprintf("%s: %d images", prefix, len(t.Images))
}

func (t *KindLoadTask) GetChildren() []tasks.Task {
	var children []tasks.Task
	for _, ref := range t.Images {
		children = append(children, &tasks.FuncTask{
			Name: ref.String(),
		})
	}
	return children
}

func (t *KindLoadTask) RetryPolicy() tasks.RetryPolicy {
	return tasks.Backoff(DefaultBackoffs)
}
