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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

var defaultTestResolver = MapScopeResolver{
	GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}:                               true,
	GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}:                                    true,
	GroupVersionKind{Group: "", Version: "v1", Kind: "ServiceAccount"}:                               true,
	GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "Role"}:                true,
	GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "RoleBinding"}:         true,
	GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"}:         false,
	GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRoleBinding"}:  false,
	GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}: false,
	GroupVersionKind{Group: "", Version: "v1", Kind: "Namespace"}:                                    false,
}

func TestRetargetManifestNamespace_NamespacedWithExplicitNamespace(t *testing.T) {
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: test-deploy
  namespace: default
spec:
  replicas: 1
`
	got, clusterScoped, err := RetargetManifestNamespace(manifest, "corp-gke", defaultTestResolver, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clusterScoped) != 0 {
		t.Errorf("expected 0 cluster-scoped objects, got: %v", clusterScoped)
	}
	if !strings.Contains(got, "namespace: corp-gke") {
		t.Errorf("expected 'namespace: corp-gke' in output, got:\n%s", got)
	}
	if strings.Contains(got, "namespace: default") {
		t.Errorf("expected original 'namespace: default' to be replaced, got:\n%s", got)
	}
}

func TestRetargetManifestNamespace_NamespacedWithoutExplicitNamespace(t *testing.T) {
	manifest := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-cm
data:
  key: value
`
	got, clusterScoped, err := RetargetManifestNamespace(manifest, "corp-gke", defaultTestResolver, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clusterScoped) != 0 {
		t.Errorf("expected 0 cluster-scoped objects, got: %v", clusterScoped)
	}
	if !strings.Contains(got, "namespace: corp-gke") {
		t.Errorf("expected 'namespace: corp-gke' to be added, got:\n%s", got)
	}
}

func TestRetargetManifestNamespace_ClusterScopedUntouched(t *testing.T) {
	manifest := `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: test-cr
rules:
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get"]
`
	got, clusterScoped, err := RetargetManifestNamespace(manifest, "corp-gke", defaultTestResolver, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clusterScoped) != 1 || clusterScoped[0] != "ClusterRole/test-cr" {
		t.Errorf("expected clusterScoped to contain 'ClusterRole/test-cr', got: %v", clusterScoped)
	}
	if strings.Contains(got, "namespace:") {
		t.Errorf("cluster-scoped object should NOT have namespace, got:\n%s", got)
	}
}

func TestRetargetManifestNamespace_ClusterRoleBindingServiceAccountSubject(t *testing.T) {
	manifest := `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: test-crb
subjects:
- kind: ServiceAccount
  name: sa-rewritten
  namespace: default
- kind: ServiceAccount
  name: sa-untouched
  namespace: kube-system
- kind: User
  name: user-untouched
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: test-cr
`
	got, clusterScoped, err := RetargetManifestNamespace(manifest, "corp-gke", defaultTestResolver, "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clusterScoped) != 1 || clusterScoped[0] != "ClusterRoleBinding/test-crb" {
		t.Errorf("expected clusterScoped to contain 'ClusterRoleBinding/test-crb', got: %v", clusterScoped)
	}
	// ClusterRoleBinding metadata itself should not have namespace
	if strings.Contains(got, "metadata:\n  namespace:") || strings.Contains(got, "metadata:\n  name: test-crb\n  namespace:") {
		t.Errorf("ClusterRoleBinding metadata should not have namespace, got:\n%s", got)
	}

	// sa-rewritten subject should have namespace: corp-gke
	if !strings.Contains(got, "name: sa-rewritten\n    namespace: corp-gke") &&
		!strings.Contains(got, "name: sa-rewritten\n  namespace: corp-gke") &&
		!strings.Contains(got, "namespace: corp-gke") {
		t.Errorf("expected sa-rewritten subject to have namespace corp-gke, got:\n%s", got)
	}

	// sa-untouched subject in kube-system must be untouched
	if !strings.Contains(got, "namespace: kube-system") {
		t.Errorf("expected sa-untouched in kube-system to remain unchanged, got:\n%s", got)
	}
}

func TestRetargetManifestNamespace_RoleBindingServiceAccountSubject(t *testing.T) {
	manifest := `apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: test-rb
  namespace: default
subjects:
- kind: ServiceAccount
  name: sa-local
  namespace: default
- kind: ServiceAccount
  name: sa-system
  namespace: kube-system
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: test-role
`
	got, clusterScoped, err := RetargetManifestNamespace(manifest, "corp-gke", defaultTestResolver, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clusterScoped) != 0 {
		t.Errorf("expected 0 cluster-scoped objects, got: %v", clusterScoped)
	}

	// RoleBinding itself should have namespace: corp-gke
	if !strings.Contains(got, "namespace: corp-gke") {
		t.Errorf("expected RoleBinding metadata to have namespace corp-gke, got:\n%s", got)
	}

	// sa-local should have namespace: corp-gke
	if strings.Contains(got, "name: sa-local\n  namespace: default") || strings.Contains(got, "name: sa-local\n    namespace: default") {
		t.Errorf("sa-local should be rewritten to corp-gke, got:\n%s", got)
	}

	// sa-system in kube-system must be untouched
	if !strings.Contains(got, "namespace: kube-system") {
		t.Errorf("expected sa-system in kube-system to remain unchanged, got:\n%s", got)
	}
}

func TestRetargetManifestNamespace_MultiDocument(t *testing.T) {
	manifest := `apiVersion: v1
kind: ServiceAccount
metadata:
  name: my-sa
  namespace: default
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-deploy
spec:
  replicas: 1
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: my-cr
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: my-crb
subjects:
- kind: ServiceAccount
  name: my-sa
  namespace: default
- kind: ServiceAccount
  name: other-sa
  namespace: kube-system
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: my-cr
`
	got, clusterScoped, err := RetargetManifestNamespace(manifest, "corp-gke", defaultTestResolver, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Cluster-scoped objects: ClusterRole and ClusterRoleBinding
	expectedClusterScoped := []string{"ClusterRole/my-cr", "ClusterRoleBinding/my-crb"}
	if len(clusterScoped) != len(expectedClusterScoped) {
		t.Fatalf("expected clusterScoped = %v, got %v", expectedClusterScoped, clusterScoped)
	}
	for i, cs := range expectedClusterScoped {
		if clusterScoped[i] != cs {
			t.Errorf("clusterScoped[%d] = %q, want %q", i, clusterScoped[i], cs)
		}
	}

	// In multi-doc, originalNamespace was automatically inferred as "default" from ServiceAccount
	if !strings.Contains(got, "name: my-sa\n  namespace: corp-gke") &&
		!strings.Contains(got, "namespace: corp-gke\n  name: my-sa") &&
		!strings.Contains(got, "namespace: corp-gke") {
		t.Errorf("expected ServiceAccount namespace rewritten to corp-gke, got:\n%s", got)
	}

	// Deployment had no namespace; it should now have namespace: corp-gke
	if !strings.Contains(got, "name: my-deploy\n  namespace: corp-gke") &&
		!strings.Contains(got, "namespace: corp-gke") {
		t.Errorf("expected Deployment namespace corp-gke, got:\n%s", got)
	}

	// ServiceAccount subject in ClusterRoleBinding was rewritten to corp-gke
	if strings.Contains(got, "name: my-sa\n    namespace: default") || strings.Contains(got, "name: my-sa\n  namespace: default") {
		t.Errorf("ClusterRoleBinding subject my-sa should be rewritten, got:\n%s", got)
	}

	// other-sa in kube-system is untouched
	if !strings.Contains(got, "namespace: kube-system") {
		t.Errorf("other-sa in kube-system should remain untouched, got:\n%s", got)
	}
}

func TestRetargetManifestNamespace_EmptyNamespaceUnchanged(t *testing.T) {
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: test-deploy
  namespace: default
`
	got, clusterScoped, err := RetargetManifestNamespace(manifest, "", defaultTestResolver, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clusterScoped) != 0 {
		t.Errorf("expected 0 cluster-scoped objects, got: %v", clusterScoped)
	}
	if got != manifest {
		t.Errorf("expected manifest to be completely unchanged when targetNamespace is empty, got:\n%s", got)
	}
}

func TestValidateNamespaceName(t *testing.T) {
	valid := []string{"", "default", "corp-gke", "kube-system", "ns-123", "a"}
	for _, ns := range valid {
		if err := ValidateNamespaceName(ns); err != nil {
			t.Errorf("expected namespace %q to be valid, got error: %v", ns, err)
		}
	}

	invalid := []string{
		"Corp-Gke",              // uppercase
		"-invalid",              // starts with hyphen
		"invalid-",              // ends with hyphen
		"in_valid",              // underscore
		"invalid.name",          // dot
		strings.Repeat("a", 64), // longer than 63 characters
	}
	for _, ns := range invalid {
		if err := ValidateNamespaceName(ns); err == nil {
			t.Errorf("expected namespace %q to be invalid, got nil error", ns)
		}
	}
}

func TestFindOriginalNamespaceInFiles(t *testing.T) {
	tempDir := t.TempDir()

	file1 := filepath.Join(tempDir, "cm.yaml")
	manifest1 := `apiVersion: v1
kind: ConfigMap
metadata:
  name: my-cm
`
	if err := os.WriteFile(file1, []byte(manifest1), 0644); err != nil {
		t.Fatal(err)
	}

	file2 := filepath.Join(tempDir, "deploy.yaml")
	manifest2 := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-deploy
  namespace: original-ns
`
	if err := os.WriteFile(file2, []byte(manifest2), 0644); err != nil {
		t.Fatal(err)
	}

	got := FindOriginalNamespaceInFiles([]string{file1, file2}, defaultTestResolver)
	if got != "original-ns" {
		t.Errorf("expected FindOriginalNamespaceInFiles = 'original-ns', got %q", got)
	}

	// When no files have a namespace:
	gotEmpty := FindOriginalNamespaceInFiles([]string{file1}, defaultTestResolver)
	if gotEmpty != "" {
		t.Errorf("expected FindOriginalNamespaceInFiles = '', got %q", gotEmpty)
	}
}

func TestKubectlApplyTask_TargetNamespaceDoesNotExist(t *testing.T) {
	tempDir := t.TempDir()

	// Create dummy ap config
	if err := os.MkdirAll(filepath.Join(tempDir, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, ".ap", "ap.yaml"), []byte("imagePrefix: test.local\n"), 0644); err != nil {
		t.Fatal(err)
	}

	manifestPath := filepath.Join(tempDir, "test.yaml")
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: test-app
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}

	task := &KubectlApplyTask{
		ManifestPath:  manifestPath,
		Namespace:     "nonexistent-ns",
		ScopeResolver: defaultTestResolver,
		CheckNamespace: func(ctx context.Context, ns string) error {
			return fmt.Errorf("target namespace %q does not exist: not found", ns)
		},
	}

	tree, err := sourcetree.Open(tempDir)
	if err != nil {
		t.Fatal(err)
	}
	scope := tasks.NewTaskContext(tempDir, tempDir, tree)
	err = task.Run(t.Context(), scope)
	if err == nil {
		t.Fatalf("expected error when namespace does not exist, got nil")
	}
	if !strings.Contains(err.Error(), "target namespace \"nonexistent-ns\" does not exist") {
		t.Errorf("expected error to mention 'target namespace \"nonexistent-ns\" does not exist', got: %v", err)
	}
}

func TestKubectlScopeResolver_ParseOutput(t *testing.T) {
	mockOutput := `NAME                              SHORTNAMES   APIVERSION                             NAMESPACED   KIND
bindings                                       v1                                     true         Binding
namespaces                        ns           v1                                     false        Namespace
pods                              po           v1                                     true         Pod
services                          svc          v1                                     true         Service
deployments                       deploy       apps/v1                                true         Deployment
clusterrolebindings                            rbac.authorization.k8s.io/v1           false        ClusterRoleBinding
clusterroles                                   rbac.authorization.k8s.io/v1           false        ClusterRole
rolebindings                                   rbac.authorization.k8s.io/v1           true         RoleBinding
roles                                          rbac.authorization.k8s.io/v1           true         Role
customresourcedefinitions         crd,crds     apiextensions.k8s.io/v1                false        CustomResourceDefinition
`

	var capturedCmdName string
	resolver := NewKubectlScopeResolver()
	resolver.KubectlBin = "/opt/tools/kubectl"
	resolver.execCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		capturedCmdName = name
		return []byte(mockOutput), nil
	}

	tests := []struct {
		gvk     GroupVersionKind
		wantNS  bool
		wantErr bool
	}{
		{gvk: GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, wantNS: true},
		{gvk: GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}, wantNS: true},
		{gvk: GroupVersionKind{Group: "", Version: "v1", Kind: "Namespace"}, wantNS: false},
		{gvk: GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRoleBinding"}, wantNS: false},
		{gvk: GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "RoleBinding"}, wantNS: true},
		{gvk: GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}, wantNS: false},
		{gvk: GroupVersionKind{Group: "custom.io", Version: "v1", Kind: "UnknownKind"}, wantErr: true},
	}

	for _, tt := range tests {
		got, err := resolver.IsNamespaced(tt.gvk)
		if (err != nil) != tt.wantErr {
			t.Errorf("IsNamespaced(%v) error = %v, wantErr = %v", tt.gvk, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.wantNS {
			t.Errorf("IsNamespaced(%v) = %v, want %v", tt.gvk, got, tt.wantNS)
		}
	}

	if capturedCmdName != "/opt/tools/kubectl" {
		t.Errorf("expected execCmd name %q, got %q", "/opt/tools/kubectl", capturedCmdName)
	}
}
