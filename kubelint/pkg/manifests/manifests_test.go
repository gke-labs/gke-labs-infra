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

package manifests

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	yamlData := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: foo
---
apiVersion: apps/v1
kind: StatefulSet
spec:
  updateStrategy:
    type: RollingUpdate
`
	objs, err := Parse(strings.NewReader(yamlData))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("Expected 2 objects, got %d", len(objs))
	}

	kind0, _, _ := objs[0].GetString("kind")
	if kind0 != "Deployment" {
		t.Errorf("Expected Deployment, got %s", kind0)
	}

	kind1, _, _ := objs[1].GetString("kind")
	if kind1 != "StatefulSet" {
		t.Errorf("Expected StatefulSet, got %s", kind1)
	}

	strategy, found, err := objs[1].GetString("spec.updateStrategy.type")
	if err != nil {
		t.Fatalf("GetString failed: %v", err)
	}
	if !found {
		t.Fatalf("Expected to find spec.updateStrategy.type")
	}
	if strategy != "RollingUpdate" {
		t.Errorf("Expected RollingUpdate, got %s", strategy)
	}

	line, err := objs[1].GetLine("spec.updateStrategy.type")
	if err != nil {
		t.Fatalf("GetLine failed: %v", err)
	}
	// Lines in yamlData:
	// 1: (empty)
	// 2: apiVersion: apps/v1
	// 3: kind: Deployment
	// 4: metadata:
	// 5:   name: foo
	// 6: ---
	// 7: apiVersion: apps/v1
	// 8: kind: StatefulSet
	// 9: spec:
	// 10:   updateStrategy:
	// 11:     type: RollingUpdate
	if line != 11 {
		t.Errorf("Expected line 11, got %d", line)
	}
}
