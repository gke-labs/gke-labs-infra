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

package rules

import (
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/kubelint/pkg/manifests"
)

func TestStatefulSetUpdateStrategy(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		wantDiag bool
	}{
		{
			name: "missing updateStrategy",
			yaml: `
apiVersion: apps/v1
kind: StatefulSet
spec:
  replicas: 3
`,
			wantDiag: true,
		},
		{
			name: "explicit RollingUpdate",
			yaml: `
apiVersion: apps/v1
kind: StatefulSet
spec:
  updateStrategy:
    type: RollingUpdate
`,
			wantDiag: false,
		},
		{
			name: "explicit OnDelete",
			yaml: `
apiVersion: apps/v1
kind: StatefulSet
spec:
  updateStrategy:
    type: OnDelete
`,
			wantDiag: false,
		},
		{
			name: "not a StatefulSet",
			yaml: `
apiVersion: apps/v1
kind: Deployment
spec:
  replicas: 3
`,
			wantDiag: false,
		},
	}

	rule := &StatefulSetUpdateStrategy{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs, err := manifests.Parse(strings.NewReader(tt.yaml))
			if err != nil {
				t.Fatalf("Parse failed: %v", err)
			}
			diags := rule.Check(objs[0])
			if tt.wantDiag && len(diags) == 0 {
				t.Errorf("Expected diagnostic, got none")
			}
			if !tt.wantDiag && len(diags) > 0 {
				t.Errorf("Expected no diagnostic, got %v", diags)
			}
		})
	}
}
