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

package k8s

import (
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/applyset"
)

func TestStampManifest(t *testing.T) {
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-app
  namespace: test-ns
spec:
  replicas: 1
---
apiVersion: v1
kind: Service
metadata:
  name: my-svc
  labels:
    app: existing
spec:
  ports:
  - port: 80
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: my-cr
rules: []
`
	resolver := MapScopeResolver{
		GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}:                       true,
		GroupVersionKind{Group: "", Version: "v1", Kind: "Service"}:                              true,
		GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"}: false,
	}

	id := "applyset-test-id-v1"
	stamped, applied, err := StampManifest(manifest, id, resolver, "fallback-ns")
	if err != nil {
		t.Fatalf("StampManifest failed: %v", err)
	}

	// Verify every document has the part-of label
	if !strings.Contains(stamped, "applyset.kubernetes.io/part-of: applyset-test-id-v1") {
		t.Errorf("stamped manifest missing part-of label:\n%s", stamped)
	}

	// Existing labels must be preserved
	if !strings.Contains(stamped, "app: existing") {
		t.Errorf("existing label was not preserved:\n%s", stamped)
	}

	// Verify applied objects
	expectedApplied := []AppliedObjectInfo{
		{GroupKind: applyset.GroupKind{Group: "apps", Kind: "Deployment"}, Namespace: "test-ns"},
		{GroupKind: applyset.GroupKind{Group: "", Kind: "Service"}, Namespace: "fallback-ns"},                   // fallback because no metadata.namespace
		{GroupKind: applyset.GroupKind{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole"}, Namespace: ""}, // cluster-scoped
	}

	if len(applied) != len(expectedApplied) {
		t.Fatalf("expected %d applied objects, got %d", len(expectedApplied), len(applied))
	}
	for i, exp := range expectedApplied {
		if applied[i].GroupKind != exp.GroupKind || applied[i].Namespace != exp.Namespace {
			t.Errorf("[%d] got %+v, want %+v", i, applied[i], exp)
		}
	}
}

func TestResolveParentRef(t *testing.T) {
	t.Run("resolves ConfigMap namespace to deploy namespace", func(t *testing.T) {
		parent := &applyset.ParentRef{
			Kind: "configmaps",
			Name: "foo",
		}
		if err := ResolveParentRef(parent, "my-ns", "", ""); err != nil {
			t.Fatal(err)
		}
		if parent.Kind != "ConfigMap" {
			t.Errorf("expected Kind = ConfigMap, got %s", parent.Kind)
		}
		if parent.Namespace != "my-ns" {
			t.Errorf("expected Namespace = my-ns, got %s", parent.Namespace)
		}
	})

	t.Run("namespaced parent without deploy namespace returns required error", func(t *testing.T) {
		parent := &applyset.ParentRef{
			Kind: "configmaps",
			Name: "foo",
		}
		err := ResolveParentRef(parent, "", "", "")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		want := "namespace is required to use namespace-scoped ApplySet"
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error %q, got %q", want, err.Error())
		}
	})
}
