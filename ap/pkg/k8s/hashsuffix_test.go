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
	"regexp"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/applyset"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/imageref"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

func TestConfigHashIndex_DeterministicAndContentSensitive(t *testing.T) {
	configMapA := `apiVersion: v1
kind: ConfigMap
metadata:
  name: eval-runner-config
data:
  alpha: "1"
  beta: "2"
`
	// Same data with reordered keys and extra comments should produce the exact same hash.
	configMapReordered := `# comment
apiVersion: v1
kind: ConfigMap
metadata:
  name: eval-runner-config
  labels:
    app: eval-runner
data:
  beta: "2"
  alpha: "1"
`
	// Modified data should produce a different hash.
	configMapChanged := `apiVersion: v1
kind: ConfigMap
metadata:
  name: eval-runner-config
data:
  alpha: "1"
  beta: "3"
`

	indexA := NewConfigHashIndex(nil)
	if err := indexA.AddContent(configMapA, ""); err != nil {
		t.Fatal(err)
	}
	resA, err := indexA.RewriteManifest(configMapA, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resA.Renames) != 1 {
		t.Fatalf("expected 1 rename, got %d", len(resA.Renames))
	}
	suffixedA := resA.Renames[0].SuffixedName
	if !regexp.MustCompile(`^eval-runner-config-[0-9a-f]{10}$`).MatchString(suffixedA) {
		t.Fatalf("unexpected suffixed name format: %q", suffixedA)
	}

	indexReordered := NewConfigHashIndex(nil)
	if err := indexReordered.AddContent(configMapReordered, ""); err != nil {
		t.Fatal(err)
	}
	resReordered, err := indexReordered.RewriteManifest(configMapReordered, "")
	if err != nil {
		t.Fatal(err)
	}
	if resReordered.Renames[0].SuffixedName != suffixedA {
		t.Errorf("reordered keys/labels changed hash: got %q, want %q", resReordered.Renames[0].SuffixedName, suffixedA)
	}

	indexChanged := NewConfigHashIndex(nil)
	if err := indexChanged.AddContent(configMapChanged, ""); err != nil {
		t.Fatal(err)
	}
	resChanged, err := indexChanged.RewriteManifest(configMapChanged, "")
	if err != nil {
		t.Fatal(err)
	}
	if resChanged.Renames[0].SuffixedName == suffixedA {
		t.Errorf("changed ConfigMap data produced identical hash %q", suffixedA)
	}
}

func TestConfigHashIndex_SecretDataStringDataAndType(t *testing.T) {
	secretBase := `apiVersion: v1
kind: Secret
metadata:
  name: app-secret
type: Opaque
stringData:
  token: "v1"
`
	secretChangedStringData := `apiVersion: v1
kind: Secret
metadata:
  name: app-secret
type: Opaque
stringData:
  token: "v2"
`
	secretChangedType := `apiVersion: v1
kind: Secret
metadata:
  name: app-secret
type: kubernetes.io/tls
stringData:
  token: "v1"
`

	hashFor := func(yamlContent string) string {
		t.Helper()
		index := NewConfigHashIndex(nil)
		if err := index.AddContent(yamlContent, ""); err != nil {
			t.Fatal(err)
		}
		res, err := index.RewriteManifest(yamlContent, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Renames) != 1 {
			t.Fatalf("expected 1 rename, got %d", len(res.Renames))
		}
		return res.Renames[0].SuffixedName
	}

	baseName := hashFor(secretBase)
	if !regexp.MustCompile(`^app-secret-[0-9a-f]{10}$`).MatchString(baseName) {
		t.Fatalf("unexpected secret suffixed name: %q", baseName)
	}
	if got := hashFor(secretChangedStringData); got == baseName {
		t.Errorf("expected different hash when stringData changes, both were %q", baseName)
	}
	if got := hashFor(secretChangedType); got == baseName {
		t.Errorf("expected different hash when Secret type changes, both were %q", baseName)
	}
}

func TestConfigHashIndex_RewriteAllReferenceTypesAndWorkloads(t *testing.T) {
	manifest := `apiVersion: v1
kind: ConfigMap
metadata:
  name: eval-runner-config
data:
  MODE: prod
---
apiVersion: v1
kind: Secret
metadata:
  name: eval-runner-secret
data:
  TOKEN: dG9rZW4=
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: eval-runner-webhook
spec:
  template:
    spec:
      imagePullSecrets:
      - name: eval-runner-secret
      - name: external-pull-secret
      initContainers:
      - name: init
        image: busybox
        envFrom:
        - configMapRef:
            name: eval-runner-config
        - secretRef:
            name: external-secret
      containers:
      - name: webhook
        image: webhook
        envFrom:
        - configMapRef:
            name: eval-runner-config
        - secretRef:
            name: eval-runner-secret
        env:
        - name: MODE
          valueFrom:
            configMapKeyRef:
              name: eval-runner-config
              key: MODE
        - name: TOKEN
          valueFrom:
            secretKeyRef:
              name: eval-runner-secret
              key: TOKEN
        - name: UNMANAGED
          valueFrom:
            configMapKeyRef:
              name: unmanaged-cm
              key: FOO
      volumes:
      - name: cfg
        configMap:
          name: eval-runner-config
      - name: sec
        secret:
          secretName: eval-runner-secret
      - name: ext-sec
        secret:
          secretName: external-secret
      - name: proj
        projected:
          sources:
          - configMap:
              name: eval-runner-config
          - secret:
              name: eval-runner-secret
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: node-agent
spec:
  template:
    spec:
      containers:
      - name: agent
        image: agent
        envFrom:
        - configMapRef:
            name: eval-runner-config
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: stateful-worker
spec:
  template:
    spec:
      volumes:
      - name: sec
        secret:
          secretName: eval-runner-secret
---
apiVersion: batch/v1
kind: Job
metadata:
  name: migrate
spec:
  template:
    spec:
      containers:
      - name: migrate
        image: migrate
        env:
        - name: TOKEN
          valueFrom:
            secretKeyRef:
              name: eval-runner-secret
              key: TOKEN
---
apiVersion: batch/v1
kind: CronJob
metadata:
  name: periodic
spec:
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: run
            image: runner
            envFrom:
            - configMapRef:
                name: eval-runner-config
---
apiVersion: v1
kind: Pod
metadata:
  name: debug-pod
spec:
  containers:
  - name: debug
    image: busybox
    envFrom:
    - configMapRef:
        name: eval-runner-config
    - secretRef:
        name: eval-runner-secret
`

	index := NewConfigHashIndex(nil)
	if err := index.AddContent(manifest, ""); err != nil {
		t.Fatal(err)
	}
	res, err := index.RewriteManifest(manifest, "")
	if err != nil {
		t.Fatal(err)
	}

	var cmSuffixed, secretSuffixed string
	for _, rename := range res.Renames {
		switch rename.Kind {
		case "ConfigMap":
			cmSuffixed = rename.SuffixedName
		case "Secret":
			secretSuffixed = rename.SuffixedName
		}
	}
	if cmSuffixed == "" || secretSuffixed == "" {
		t.Fatalf("expected both ConfigMap and Secret renames, got %+v", res.Renames)
	}

	// No bare unsuffixed references to managed ConfigMap/Secret should remain.
	bareCM := regexp.MustCompile(`\beval-runner-config(\s|$)`)
	if bareCM.MatchString(res.Content) {
		t.Errorf("found unrewritten reference to eval-runner-config:\n%s", res.Content)
	}
	bareSecret := regexp.MustCompile(`\beval-runner-secret(\s|$)`)
	if bareSecret.MatchString(res.Content) {
		t.Errorf("found unrewritten reference to eval-runner-secret:\n%s", res.Content)
	}

	// Every workload and reference type should point to the suffixed names.
	// ConfigMap appears in: ConfigMap.metadata.name (1), Deployment initContainers envFrom (1),
	// Deployment containers envFrom (1), env.valueFrom (1), volumes.configMap (1),
	// volumes.projected.configMap (1), DaemonSet (1), CronJob (1), Pod (1) = 9 times.
	if got := strings.Count(res.Content, cmSuffixed); got != 9 {
		t.Errorf("expected %q to appear 9 times, got %d:\n%s", cmSuffixed, got, res.Content)
	}
	// Secret appears in: Secret.metadata.name (1), Deployment imagePullSecrets (1),
	// Deployment containers envFrom (1), env.valueFrom (1), volumes.secret (1),
	// volumes.projected.secret (1), StatefulSet (1), Job (1), Pod (1) = 9 times.
	if got := strings.Count(res.Content, secretSuffixed); got != 9 {
		t.Errorf("expected %q to appear 9 times, got %d:\n%s", secretSuffixed, got, res.Content)
	}

	// Unmanaged references must remain untouched.
	for _, unmanaged := range []string{"external-pull-secret", "external-secret", "unmanaged-cm"} {
		if !strings.Contains(res.Content, unmanaged) {
			t.Errorf("expected unmanaged resource %q to remain untouched:\n%s", unmanaged, res.Content)
		}
	}
}

func TestConfigHashIndex_NamespaceIsolation(t *testing.T) {
	manifest := `apiVersion: v1
kind: ConfigMap
metadata:
  name: shared-name
  namespace: ns-a
data:
  env: a
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: shared-name
  namespace: ns-b
data:
  env: b
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: app-a
  namespace: ns-a
spec:
  template:
    spec:
      containers:
      - name: app
        image: app
        envFrom:
        - configMapRef:
            name: shared-name
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: app-b
  namespace: ns-b
spec:
  template:
    spec:
      containers:
      - name: app
        image: app
        envFrom:
        - configMapRef:
            name: shared-name
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: app-other
  namespace: ns-c
spec:
  template:
    spec:
      containers:
      - name: app
        image: app
        envFrom:
        - configMapRef:
            name: shared-name
`
	index := NewConfigHashIndex(nil)
	if err := index.AddContent(manifest, ""); err != nil {
		t.Fatal(err)
	}
	res, err := index.RewriteManifest(manifest, "")
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Renames) != 2 {
		t.Fatalf("expected 2 renames, got %+v", res.Renames)
	}
	nameA := res.Renames[0].SuffixedName
	nameB := res.Renames[1].SuffixedName
	if nameA == nameB {
		t.Fatalf("expected distinct hashes for ns-a and ns-b ConfigMaps, both got %q", nameA)
	}

	// Each suffixed name should appear twice: its ConfigMap and its same-namespace Deployment.
	if got := strings.Count(res.Content, nameA); got != 2 {
		t.Errorf("expected ns-a suffixed name %q twice, got %d:\n%s", nameA, got, res.Content)
	}
	if got := strings.Count(res.Content, nameB); got != 2 {
		t.Errorf("expected ns-b suffixed name %q twice, got %d:\n%s", nameB, got, res.Content)
	}
	// ns-c has no ConfigMap in manifests, so its bare reference must stay untouched.
	if got := strings.Count(res.Content, "name: shared-name\n"); got != 1 {
		t.Errorf("expected ns-c Deployment to retain bare reference once, got %d:\n%s", got, res.Content)
	}
}

func TestDeployTasksForManifests_HashSuffixCrossFileAndImagePlaceholder(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tempDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, ".ap"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, "k8s"), 0755); err != nil {
		t.Fatal(err)
	}

	// Deployment file sorts BEFORE ConfigMap file to prove cross-file pre-indexing works.
	deployPath := filepath.Join(tempDir, "k8s", "a-deployment.yaml")
	deployYAML := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: eval-runner-webhook
  namespace: original-ns
spec:
  template:
    spec:
      containers:
      - name: webhook
        image: webhook
        envFrom:
        - configMapRef:
            name: eval-runner-config
`
	if err := os.WriteFile(deployPath, []byte(deployYAML), 0644); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(tempDir, "k8s", "z-configmap.yaml")
	configYAML := `apiVersion: v1
kind: ConfigMap
metadata:
  name: eval-runner-config
  namespace: original-ns
data:
  sidecar.image: "${IMAGE_PREFIX}/sidecar:${IMAGE_TAG}"
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}

	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	appliedDir := filepath.Join(tempDir, "applied")
	if err := os.MkdirAll(appliedDir, 0755); err != nil {
		t.Fatal(err)
	}
	counterFile := filepath.Join(tempDir, "counter")
	fakeKubectl := filepath.Join(binDir, "kubectl")
	fakeKubectlScript := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "apply" ]; then
  count=0
  if [ -f "%s" ]; then
    count=$(cat "%s")
  fi
  count=$((count + 1))
  echo "$count" > "%s"
  cat > "%s/$count.yaml"
  exit 0
fi
exit 0
`, counterFile, counterFile, counterFile, appliedDir)
	if err := os.WriteFile(fakeKubectl, []byte(fakeKubectlScript), 0755); err != nil {
		t.Fatal(err)
	}

	tree, err := sourcetree.Open(t.Context(), tempDir)
	if err != nil {
		t.Fatal(err)
	}

	runDeploy := func(digest string) (string, string) {
		t.Helper()
		_ = os.Remove(counterFile)
		naming := imageref.NewImageNaming("example.com/repo", "latest", false)
		naming.Ref("sidecar").Digest = digest

		scope := tasks.NewTaskContext(tempDir, tempDir, tree).WithTool("kubectl", fakeKubectl)
		membership := &applyset.Membership{}
		group := DeployTasksForManifests([]string{deployPath, configPath}, DeployTaskOptions{
			RepoRoot:          tempDir,
			Namespace:         "target-ns",
			OriginalNamespace: "original-ns",
			ScopeResolver:     defaultTestResolver,
			CheckNamespace: func(ctx context.Context, namespace string) error {
				return nil
			},
			Naming:     naming,
			ApplySetID: "applyset-test-v1",
			Membership: membership,
			HashSuffix: true,
		})
		if err := group.Run(t.Context(), scope); err != nil {
			t.Fatalf("deploy group failed: %v", err)
		}

		appliedDeploy, err := os.ReadFile(filepath.Join(appliedDir, "1.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		appliedConfig, err := os.ReadFile(filepath.Join(appliedDir, "2.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		return string(appliedDeploy), string(appliedConfig)
	}

	deploy1, config1 := runDeploy("sha256:1111111111111111111111111111111111111111111111111111111111111111")
	namePattern := regexp.MustCompile(`eval-runner-config-[0-9a-f]{10}`)
	matchDeploy1 := namePattern.FindString(deploy1)
	matchConfig1 := namePattern.FindString(config1)
	if matchDeploy1 == "" || matchDeploy1 != matchConfig1 {
		t.Fatalf("expected matching suffixed name across Deployment (%q) and ConfigMap (%q)\ndeploy:\n%s\nconfig:\n%s", matchDeploy1, matchConfig1, deploy1, config1)
	}
	if !strings.Contains(deploy1, "namespace: target-ns") || !strings.Contains(config1, "namespace: target-ns") {
		t.Errorf("expected namespace retargeting to target-ns in both manifests")
	}
	if !strings.Contains(config1, "applyset.kubernetes.io/part-of: applyset-test-v1") {
		t.Errorf("expected ApplySet label on suffixed ConfigMap:\n%s", config1)
	}

	// Rebuilding the sidecar image with a new digest changes the resolved ConfigMap data,
	// so the ConfigMap hash suffix (and Deployment reference) must rotate too.
	deploy2, config2 := runDeploy("sha256:2222222222222222222222222222222222222222222222222222222222222222")
	matchDeploy2 := namePattern.FindString(deploy2)
	matchConfig2 := namePattern.FindString(config2)
	if matchDeploy2 == "" || matchDeploy2 != matchConfig2 {
		t.Fatalf("expected matching suffixed name on second deploy: %q vs %q", matchDeploy2, matchConfig2)
	}
	if matchDeploy2 == matchDeploy1 {
		t.Errorf("expected ConfigMap hash suffix to change when referenced image digest changes, both were %q", matchDeploy1)
	}
}
