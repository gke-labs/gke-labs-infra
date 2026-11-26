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
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/imageref"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

func TestConfigHash_DeterminismAndChanges(t *testing.T) {
	hashOf := func(t *testing.T, manifest string) string {
		t.Helper()
		renames := make(ConfigHashMap)
		if err := indexConfigHashes(renames, manifest, ""); err != nil {
			t.Fatal(err)
		}
		for _, entry := range renames {
			return entry.suffixedName
		}
		t.Fatal("expected one indexed config resource")
		return ""
	}

	baseCM := "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: app-cfg, annotations: {" + HashSuffixAnnotation + ": 'true'}}\ndata: {a: '1', b: '2'}\n"
	baseSecret := "apiVersion: v1\nkind: Secret\nmetadata: {name: app-sec, annotations: {" + HashSuffixAnnotation + ": 'true'}}\ntype: Opaque\nstringData: {tok: v1}\n"

	tests := []struct {
		name     string
		a, b     string
		wantSame bool
	}{
		{"reordered keys and labels", baseCM, "# c\napiVersion: v1\nkind: ConfigMap\nmetadata: {name: app-cfg, labels: {x: y}, annotations: {" + HashSuffixAnnotation + ": 'true'}}\ndata: {b: '2', a: '1'}\n", true},
		{"changed ConfigMap data", baseCM, "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: app-cfg, annotations: {" + HashSuffixAnnotation + ": 'true'}}\ndata: {a: '1', b: '3'}\n", false},
		{"changed Secret stringData", baseSecret, "apiVersion: v1\nkind: Secret\nmetadata: {name: app-sec, annotations: {" + HashSuffixAnnotation + ": 'true'}}\ntype: Opaque\nstringData: {tok: v2}\n", false},
		{"changed Secret type", baseSecret, "apiVersion: v1\nkind: Secret\nmetadata: {name: app-sec, annotations: {" + HashSuffixAnnotation + ": 'true'}}\ntype: kubernetes.io/tls\nstringData: {tok: v1}\n", false},
	}

	suffixPattern := regexp.MustCompile(`^app-(cfg|sec)-[0-9a-f]{10}$`)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nameA := hashOf(t, tc.a)
			nameB := hashOf(t, tc.b)
			if !suffixPattern.MatchString(nameA) {
				t.Fatalf("unexpected suffixed name format: %q", nameA)
			}
			if (nameA == nameB) != tc.wantSame {
				t.Errorf("got %q vs %q, wantSame=%v", nameA, nameB, tc.wantSame)
			}
		})
	}

	for _, badVal := range []string{`"false"`, `""`, `true`} {
		t.Run("invalid annotation "+badVal, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bad.yaml")
			content := fmt.Sprintf("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app-cfg\n  annotations:\n    %s: %s\n", HashSuffixAnnotation, badVal)
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := BuildConfigHashMap([]string{path}, imageref.ImageNaming{}, "")
			if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "ConfigMap/app-cfg") {
				t.Fatalf("expected error naming file and ConfigMap/app-cfg, got: %v", err)
			}
		})
	}
}

func TestRewriteConfigHashes_ReferenceShapesAndWorkloads(t *testing.T) {
	renames := ConfigHashMap{
		{kind: "ConfigMap", namespace: "default", name: "app-cfg"}: {suffixedName: "app-cfg-1111111111"},
		{kind: "Secret", namespace: "default", name: "app-sec"}:    {suffixedName: "app-sec-2222222222"},
	}

	tests := []struct {
		name     string
		manifest string
		want     []string
		absent   []string
	}{
		{
			name: "Deployment all reference shapes",
			manifest: `apiVersion: apps/v1
kind: Deployment
metadata: {name: web}
spec:
  template:
    spec:
      imagePullSecrets: [{name: app-sec}, {name: external-sec}]
      initContainers:
      - name: init
        envFrom: [{configMapRef: {name: app-cfg}}]
      containers:
      - name: app
        envFrom: [{configMapRef: {name: app-cfg}}, {secretRef: {name: app-sec}}]
        env:
        - {name: K1, valueFrom: {configMapKeyRef: {name: app-cfg, key: k}}}
        - {name: K2, valueFrom: {secretKeyRef: {name: app-sec, key: k}}}
        - {name: EXT, valueFrom: {configMapKeyRef: {name: external-cfg, key: k}}}
      volumes:
      - {name: v1, configMap: {name: app-cfg}}
      - {name: v2, secret: {secretName: app-sec}}
      - {name: v3, projected: {sources: [{configMap: {name: app-cfg}}, {secret: {name: app-sec}}]}}
      - {name: v4, cephfs: {secretRef: {name: app-sec}}}
`,
			want:   []string{"app-cfg-1111111111", "app-sec-2222222222", "external-cfg", "external-sec"},
			absent: []string{"name: app-cfg\n", "name: app-sec\n", "secretName: app-sec\n"},
		},
		{"DaemonSet", "apiVersion: apps/v1\nkind: DaemonSet\nmetadata: {name: ds}\nspec: {template: {spec: {containers: [{name: c, envFrom: [{configMapRef: {name: app-cfg}}]}]}}}\n", []string{"app-cfg-1111111111"}, nil},
		{"StatefulSet", "apiVersion: apps/v1\nkind: StatefulSet\nmetadata: {name: ss}\nspec: {template: {spec: {volumes: [{name: s, secret: {secretName: app-sec}}]}}}\n", []string{"app-sec-2222222222"}, nil},
		{"Job", "apiVersion: batch/v1\nkind: Job\nmetadata: {name: j}\nspec: {template: {spec: {containers: [{name: c, envFrom: [{secretRef: {name: app-sec}}]}]}}}\n", []string{"app-sec-2222222222"}, nil},
		{"CronJob", "apiVersion: batch/v1\nkind: CronJob\nmetadata: {name: cj}\nspec: {jobTemplate: {spec: {template: {spec: {containers: [{name: c, envFrom: [{configMapRef: {name: app-cfg}}]}]}}}}}\n", []string{"app-cfg-1111111111"}, nil},
		{"Pod", "apiVersion: v1\nkind: Pod\nmetadata: {name: p}\nspec: {containers: [{name: c, envFrom: [{configMapRef: {name: app-cfg}}]}]}\n", []string{"app-cfg-1111111111"}, nil},
		{"different namespace untouched", "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: d, namespace: other}\nspec: {template: {spec: {containers: [{name: c, envFrom: [{configMapRef: {name: app-cfg}}]}]}}}\n", []string{"name: app-cfg"}, []string{"app-cfg-1111111111"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := rewriteConfigHashes(tc.manifest, "", renames)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("expected %q in output:\n%s", w, got)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(got, a) {
					t.Errorf("did not expect %q in output:\n%s", a, got)
				}
			}
		})
	}
}

func TestDeployTasksForManifests_HashSuffixAcrossFiles(t *testing.T) {
	tempDir := t.TempDir()
	for _, dir := range []string{".git", ".ap", "k8s", "bin", "applied"} {
		if err := os.MkdirAll(filepath.Join(tempDir, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}

	// Deployment sorts before ConfigMap to verify pre-indexing covers cross-file references.
	deployPath := filepath.Join(tempDir, "k8s", "a-deployment.yaml")
	deployYAML := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: webhook
  namespace: orig
spec:
  template:
    spec:
      containers:
      - name: webhook
        image: webhook
        envFrom:
        - configMapRef:
            name: eval-runner-config
        - configMapRef:
            name: fixed-config
`
	if err := os.WriteFile(deployPath, []byte(deployYAML), 0644); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(tempDir, "k8s", "z-configmap.yaml")
	appliedDir := filepath.Join(tempDir, "applied")
	counterFile := filepath.Join(tempDir, "counter")
	fakeKubectl := filepath.Join(tempDir, "bin", "kubectl")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "apply" ]; then
  n=$(cat %q 2>/dev/null || echo 0)
  n=$((n + 1))
  echo "$n" > %q
  cat > %q/"$n.yaml"
fi
`, counterFile, counterFile, appliedDir)
	if err := os.WriteFile(fakeKubectl, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	tree, err := sourcetree.Open(t.Context(), tempDir)
	if err != nil {
		t.Fatal(err)
	}

	deployWithData := func(value string) (string, string) {
		t.Helper()
		_ = os.Remove(counterFile)
		configYAML := fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: eval-runner-config
  namespace: orig
  annotations:
    %s: "true"
data:
  setting: %q
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: fixed-config
  namespace: orig
data:
  fixed: "yes"
`, HashSuffixAnnotation, value)
		if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
			t.Fatal(err)
		}
		naming := imageref.NewImageNaming("example.com/repo", "latest", false)
		hashes, err := BuildConfigHashMap([]string{deployPath, configPath}, naming, "target-ns")
		if err != nil {
			t.Fatal(err)
		}
		scope := tasks.NewTaskContext(tempDir, tempDir, tree).WithTool("kubectl", fakeKubectl)
		group := DeployTasksForManifests([]string{deployPath, configPath}, DeployTaskOptions{
			RepoRoot:          tempDir,
			Namespace:         "target-ns",
			OriginalNamespace: "orig",
			ScopeResolver:     defaultTestResolver,
			CheckNamespace:    func(context.Context, string) error { return nil },
			Naming:            naming,
			ConfigHashes:      hashes,
		})
		if err := group.Run(t.Context(), scope); err != nil {
			t.Fatalf("deploy group failed: %v", err)
		}
		d, err := os.ReadFile(filepath.Join(appliedDir, "1.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		c, err := os.ReadFile(filepath.Join(appliedDir, "2.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		return string(d), string(c)
	}

	pattern := regexp.MustCompile(`eval-runner-config-[0-9a-f]{10}`)
	deploy1, config1 := deployWithData("v1")
	name1 := pattern.FindString(config1)
	if name1 == "" || pattern.FindString(deploy1) != name1 {
		t.Fatalf("expected matching suffixed name across Deployment and ConfigMap:\ndeploy:\n%s\nconfig:\n%s", deploy1, config1)
	}
	if !strings.Contains(config1, HashSuffixAnnotation+": \"true\"") {
		t.Errorf("expected %s annotation to remain on applied ConfigMap:\n%s", HashSuffixAnnotation, config1)
	}
	if !strings.Contains(config1, HashSuffixOfLabel+": eval-runner-config") {
		t.Errorf("expected %s label on suffixed ConfigMap:\n%s", HashSuffixOfLabel, config1)
	}
	if strings.Count(config1, HashSuffixOfLabel) != 1 {
		t.Errorf("expected %s label only on suffixed ConfigMap, got:\n%s", HashSuffixOfLabel, config1)
	}
	if !strings.Contains(config1, "name: fixed-config\n") || !strings.Contains(deploy1, "name: fixed-config\n") {
		t.Errorf("expected unannotated fixed-config to keep its name and reference:\ndeploy:\n%s\nconfig:\n%s", deploy1, config1)
	}
	deploy2, config2 := deployWithData("v2")
	name2 := pattern.FindString(config2)
	if name2 == "" || pattern.FindString(deploy2) != name2 || name2 == name1 {
		t.Fatalf("expected rotated matching suffixed name on v2: v1=%q v2=%q", name1, name2)
	}
}

func TestSelectSupersededConfigs(t *testing.T) {
	item := func(name, namespace string, labels map[string]string) labeledObject {
		var object labeledObject
		object.Metadata.Name = name
		object.Metadata.Namespace = namespace
		object.Metadata.Labels = labels
		return object
	}

	items := []labeledObject{
		item("app-config-cccccccccc", "app-ns", map[string]string{HashSuffixOfLabel: "app-config"}),
		item("app-config-bbbbbbbbbb", "app-ns", map[string]string{HashSuffixOfLabel: "app-config"}),
		item("app-config-aaaaaaaaaa", "app-ns", map[string]string{HashSuffixOfLabel: "app-config"}),
		item("other-config-1111111111", "app-ns", map[string]string{HashSuffixOfLabel: "other-config"}),
		item("app-config", "app-ns", nil),
		item("app-config-manual", "app-ns", map[string]string{"app": "web"}),
	}

	got := selectSupersededConfigs(items, "app-config", "app-config-cccccccccc")
	want := []configObject{
		{Name: "app-config-aaaaaaaaaa", Namespace: "app-ns"},
		{Name: "app-config-bbbbbbbbbb", Namespace: "app-ns"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("selectSupersededConfigs got %v, want %v", got, want)
	}
}

func TestRemoveSupersededConfigTask(t *testing.T) {
	tempDir := t.TempDir()
	for _, dir := range []string{".git", ".ap", "bin"} {
		if err := os.MkdirAll(filepath.Join(tempDir, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}

	callsFile := filepath.Join(tempDir, "calls.log")
	fakeKubectl := filepath.Join(tempDir, "bin", "kubectl")
	script := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %q
for arg in "$@"; do
  if [ "$arg" = "get" ]; then
    cat <<'EOF'
{"items":[
  {"metadata":{"name":"app-config-new","namespace":"target-ns","labels":{"%s":"app-config"}}},
  {"metadata":{"name":"app-config-old","namespace":"target-ns","labels":{"%s":"app-config"}}},
  {"metadata":{"name":"app-config-unlabeled","namespace":"target-ns"}}
]}
EOF
    exit 0
  fi
done
`, callsFile, HashSuffixOfLabel, HashSuffixOfLabel)
	if err := os.WriteFile(fakeKubectl, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	tree, err := sourcetree.Open(t.Context(), tempDir)
	if err != nil {
		t.Fatal(err)
	}
	var logBuffer bytes.Buffer
	ctx := scoped.WithLog(t.Context(), tasks.NewLogger(&logBuffer))
	scope := tasks.NewTaskContext(tempDir, tempDir, tree).WithTool("kubectl", fakeKubectl)

	task := &RemoveSupersededConfigTask{
		Config: SuffixedConfig{
			Kind:         "ConfigMap",
			Namespace:    "target-ns",
			OriginalName: "app-config",
			CurrentName:  "app-config-new",
		},
		Cluster: ClusterFromContext("my-ctx"),
	}
	if err := task.Run(ctx, scope); err != nil {
		t.Fatalf("RemoveSupersededConfigTask.Run failed: %v", err)
	}

	callsBytes, err := os.ReadFile(callsFile)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(string(callsBytes)), "\n")
	wantCalls := []string{
		"--context my-ctx -n target-ns get configmap -l " + HashSuffixOfLabel + "=app-config -o json",
		"--context my-ctx -n target-ns delete configmap app-config-old --ignore-not-found=true",
	}
	if !slices.Equal(calls, wantCalls) {
		t.Errorf("kubectl calls got:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(wantCalls, "\n"))
	}
	wantLog := "removed superseded config kind=ConfigMap namespace=target-ns name=app-config-old\n"
	if logBuffer.String() != wantLog {
		t.Errorf("log output got %q, want %q", logBuffer.String(), wantLog)
	}
}
