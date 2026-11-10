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
	"path/filepath"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/spec"
)

// NewTool produces the Tool registry entry for the given Spec with the standard PATH-prepend Prepare.
func NewTool(s *spec.Spec) *Tool {
	return &Tool{
		Name:       s.Name,
		Short:      fmt.Sprintf("Run %s with pinned binary", s.Name),
		UpdateFlag: s.Name,
		UpdateDesc: fmt.Sprintf("Update %s versions in ap/pkg/tool/%s/versions.yaml", s.Name, s.Name),
		Ensure: func(ctx context.Context, repoRoot string) (string, error) {
			return spec.EnsureForRepo(ctx, s, repoRoot)
		},
		Prepare: func(ctx context.Context, repoRoot string, args []string) (*Invocation, error) {
			bin, err := spec.EnsureForRepo(ctx, s, repoRoot)
			if err != nil {
				return nil, fmt.Errorf("failed to prepare %s: %w", s.Name, err)
			}
			return &Invocation{
				Binary: bin,
				Args:   args,
				Env:    fileutils.PrependPath(os.Environ(), filepath.Dir(bin)),
			}, nil
		},
		Update: func(ctx context.Context, repoRoot string) error {
			return spec.Update(ctx, s, repoRoot)
		},
	}
}
