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
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/applyset"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

func TestFindManifests(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ap-deploy-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name     string
		files    []string
		expected []string
	}{
		{
			name: "legacy manifest.yaml",
			files: []string{
				"k8s/manifest.yaml",
				"not-k8s/manifest.yaml",
			},
			expected: []string{
				"k8s/manifest.yaml",
			},
		},
		{
			name: "recursive k8s",
			files: []string{
				"k8s/manifest.yaml",
				"k8s/sub/resource.yaml",
				"k8s/crds/crd.yml",
			},
			expected: []string{
				"k8s/crds/crd.yml",
				"k8s/manifest.yaml",
				"k8s/sub/resource.yaml",
			},
		},
		{
			name: "mixed files",
			files: []string{
				"k8s/resource.yaml",
				"k8s/readme.md",
				"k8s/script.sh",
			},
			expected: []string{
				"k8s/resource.yaml",
			},
		},
		{
			name: "nested k8s directory",
			files: []string{
				"pkg/something/k8s/resource.yaml",
			},
			expected: []string{
				"pkg/something/k8s/resource.yaml",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(tmpDir, tt.name)
			if err := os.MkdirAll(root, 0755); err != nil {
				t.Fatal(err)
			}

			for _, f := range tt.files {
				path := filepath.Join(root, f)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("content"), 0644); err != nil {
					t.Fatal(err)
				}
			}

			r, err := sourcetree.Open(root)
			if err != nil {
				t.Fatalf("sourcetree.Open failed: %v", err)
			}

			got, err := findManifests(r, root)
			if err != nil {
				t.Fatalf("findManifests() error = %v", err)
			}

			var gotRel []string
			for _, g := range got {
				rel, err := filepath.Rel(root, g)
				if err != nil {
					t.Fatal(err)
				}
				gotRel = append(gotRel, rel)
			}
			sort.Strings(gotRel)
			sort.Strings(tt.expected)

			if len(gotRel) != len(tt.expected) {
				t.Errorf("got %v, want %v", gotRel, tt.expected)
				return
			}
			for i := range gotRel {
				if gotRel[i] != tt.expected[i] {
					t.Errorf("at index %d: got %s, want %s", i, gotRel[i], tt.expected[i])
				}
			}
		})
	}
}

func TestFindReferencedImagesInFile_Kustomization(t *testing.T) {
	tempDir := t.TempDir()

	// cert-manager kustomization without local placeholder images
	certDir := filepath.Join(tempDir, "cert-manager")
	if err := os.MkdirAll(certDir, 0755); err != nil {
		t.Fatal(err)
	}
	certKust := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cert-manager.yaml
`
	certYaml := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: cert-manager
spec:
  template:
    spec:
      containers:
      - name: cert-manager
        image: quay.io/jetstack/cert-manager-controller:v1.17.0
`
	if err := os.WriteFile(filepath.Join(certDir, "kustomization.yaml"), []byte(certKust), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certDir, "cert-manager.yaml"), []byte(certYaml), 0644); err != nil {
		t.Fatal(err)
	}

	imgs, err := FindReferencedImagesInFile(filepath.Join(certDir, "kustomization.yaml"))
	if err != nil {
		t.Fatalf("FindReferencedImagesInFile failed: %v", err)
	}
	if len(imgs) != 0 {
		t.Errorf("expected 0 images, got %v", imgs)
	}

	// app kustomization referencing local resource with placeholder images
	appDir := filepath.Join(tempDir, "app")
	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatal(err)
	}
	appKust := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - deployment.yaml
`
	appYaml := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
      - name: worker
        image: worker
      - name: sidecar
        image: ${IMAGE_PREFIX}/sidecar:${IMAGE_TAG}
`
	if err := os.WriteFile(filepath.Join(appDir, "kustomization.yaml"), []byte(appKust), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "deployment.yaml"), []byte(appYaml), 0644); err != nil {
		t.Fatal(err)
	}

	appImgs, err := FindReferencedImagesInFile(filepath.Join(appDir, "kustomization.yaml"))
	if err != nil {
		t.Fatalf("FindReferencedImagesInFile failed: %v", err)
	}
	expectedAppImgs := []string{"sidecar", "worker"}
	if !reflect.DeepEqual(appImgs, expectedAppImgs) {
		t.Errorf("FindReferencedImagesInFile = %v, want %v", appImgs, expectedAppImgs)
	}
}

func TestKubectlApplyTask_Kustomization(t *testing.T) {
	tempDir := t.TempDir()

	// Repo root scaffolding (.ap/ap.yaml)
	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, ".ap", "ap.yaml"), []byte("imagePrefix: test.local\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create cert-manager kustomization directory
	kustDir := filepath.Join(tempDir, "cert-manager")
	if err := os.MkdirAll(kustDir, 0755); err != nil {
		t.Fatal(err)
	}
	kustPath := filepath.Join(kustDir, "kustomization.yaml")
	if err := os.WriteFile(kustPath, []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create fake bin dir
	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}

	logFile := filepath.Join(tempDir, "invocations.log")
	applyStdinFile := filepath.Join(tempDir, "apply_stdin.yaml")

	// Fake kustomize script outputs multi-doc YAML with a ConfigMap and a CustomResourceDefinition
	fakeKustomize := filepath.Join(binDir, "kustomize")
	fakeKustomizeScript := fmt.Sprintf(`#!/bin/sh
echo "kustomize $@" >> "%s"
if [ "$1" != "build" ]; then
  echo "unexpected kustomize args: $@" >&2
  exit 1
fi
cat << 'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: cert-manager-config
  namespace: original-ns
data:
  key: value
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: certificates.cert-manager.io
spec:
  group: cert-manager.io
  names:
    kind: Certificate
EOF
`, logFile)
	if err := os.WriteFile(fakeKustomize, []byte(fakeKustomizeScript), 0755); err != nil {
		t.Fatal(err)
	}

	// Fake kubectl script captures stdin on apply -f - and records commands
	fakeKubectl := filepath.Join(binDir, "kubectl")
	fakeKubectlScript := fmt.Sprintf(`#!/bin/sh
echo "kubectl $@" >> "%s"
if [ "$1" = "apply" ]; then
  cat > "%s"
  exit 0
fi
if [ "$1" = "wait" ]; then
  exit 0
fi
exit 0
`, logFile, applyStdinFile)
	if err := os.WriteFile(fakeKubectl, []byte(fakeKubectlScript), 0755); err != nil {
		t.Fatal(err)
	}

	tree, err := sourcetree.Open(tempDir)
	if err != nil {
		t.Fatal(err)
	}

	scope := tasks.NewTaskContext(tempDir, tempDir, tree)
	scope = scope.WithTool("kustomize", fakeKustomize).WithTool("kubectl", fakeKubectl)

	membership := &applyset.Membership{}
	task := &KubectlApplyTask{
		ManifestPath:      kustPath,
		Namespace:         "custom-ns",
		OriginalNamespace: "original-ns",
		ApplySetID:        "applyset-test-parent-v1",
		Membership:        membership,
		ScopeResolver:     defaultTestResolver,
		CheckNamespace: func(ctx context.Context, ns string) error {
			return nil
		},
	}

	if err := task.Run(t.Context(), scope); err != nil {
		t.Fatalf("task.Run failed: %v", err)
	}

	// Verify invocations
	invocationsData, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read logFile: %v", err)
	}
	invocations := string(invocationsData)

	if !strings.Contains(invocations, fmt.Sprintf("kustomize build %s", kustDir)) {
		t.Errorf("expected kustomize build %s, got:\n%s", kustDir, invocations)
	}
	if !strings.Contains(invocations, "kubectl apply -f -") {
		t.Errorf("expected kubectl apply -f -, got:\n%s", invocations)
	}
	if !strings.Contains(invocations, "kubectl wait --for=condition=Established customresourcedefinition/certificates.cert-manager.io --timeout=60s") {
		t.Errorf("expected kubectl wait for CRD, got:\n%s", invocations)
	}

	// Verify applied content had namespace retargeted and ApplySet label stamped
	appliedData, err := os.ReadFile(applyStdinFile)
	if err != nil {
		t.Fatalf("failed to read applyStdinFile: %v", err)
	}
	appliedYAML := string(appliedData)

	if !strings.Contains(appliedYAML, "applyset.kubernetes.io/part-of: applyset-test-parent-v1") {
		t.Errorf("expected applied YAML to contain applyset part-of label, got:\n%s", appliedYAML)
	}
	if !strings.Contains(appliedYAML, "namespace: custom-ns") {
		t.Errorf("expected applied YAML to have namespace retargeted to custom-ns, got:\n%s", appliedYAML)
	}

	// Verify membership recorded the objects
	gks := membership.GroupKinds
	if len(gks) != 2 {
		t.Errorf("expected 2 GroupKinds in membership, got %d (%v)", len(gks), gks)
	}
}

func TestKubectlApplyTask_KustomizationBuildFailure(t *testing.T) {
	tempDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, ".ap", "ap.yaml"), []byte("imagePrefix: test.local\n"), 0644); err != nil {
		t.Fatal(err)
	}

	kustDir := filepath.Join(tempDir, "broken")
	if err := os.MkdirAll(kustDir, 0755); err != nil {
		t.Fatal(err)
	}
	kustPath := filepath.Join(kustDir, "kustomization.yaml")
	if err := os.WriteFile(kustPath, []byte("invalid content"), 0644); err != nil {
		t.Fatal(err)
	}

	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}

	fakeKustomize := filepath.Join(binDir, "kustomize")
	fakeKustomizeScript := `#!/bin/sh
echo "kustomize syntax error in file" >&2
exit 1
`
	if err := os.WriteFile(fakeKustomize, []byte(fakeKustomizeScript), 0755); err != nil {
		t.Fatal(err)
	}

	fakeKubectl := filepath.Join(binDir, "kubectl")
	if err := os.WriteFile(fakeKubectl, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}

	tree, err := sourcetree.Open(tempDir)
	if err != nil {
		t.Fatal(err)
	}

	scope := tasks.NewTaskContext(tempDir, tempDir, tree)
	scope = scope.WithTool("kustomize", fakeKustomize).WithTool("kubectl", fakeKubectl)

	task := &KubectlApplyTask{
		ManifestPath: kustPath,
	}

	err = task.Run(t.Context(), scope)
	if err == nil {
		t.Fatalf("expected error on kustomize build failure, got nil")
	}
	if !strings.Contains(err.Error(), "kustomize build failed") || !strings.Contains(err.Error(), "kustomize syntax error in file") {
		t.Errorf("expected error to mention kustomize build failed and stderr, got: %v", err)
	}
}
