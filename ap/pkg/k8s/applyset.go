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
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/applyset"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/version"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"
)

// AppliedObjectInfo records the GroupKind and namespace of an applied object.
type AppliedObjectInfo struct {
	GroupKind applyset.GroupKind
	Namespace string
}

// StampManifest decodes YAML content, adds applyset.kubernetes.io/part-of label
// to every object, and returns the modified YAML along with applied object infos.
func StampManifest(content string, id string, resolver ScopeResolver, fallbackNamespace string) (string, []AppliedObjectInfo, error) {
	decoder := yaml.NewDecoder(strings.NewReader(content))
	var docs []*yaml.Node
	for {
		var doc yaml.Node
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", nil, fmt.Errorf("failed to decode YAML: %w", err)
		}
		docs = append(docs, &doc)
	}

	if len(docs) == 0 {
		return content, nil, nil
	}

	var applied []AppliedObjectInfo

	for _, doc := range docs {
		if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
			continue
		}
		root := doc.Content[0]
		if root.Kind != yaml.MappingNode {
			continue
		}

		apiVersion, kind := getAPIVersionAndKind(root)
		if kind == "" {
			continue
		}

		gvk := parseGVK(apiVersion, kind)
		gk := applyset.GroupKind{Group: gvk.Group, Kind: gvk.Kind}

		ns := getMetadataNamespace(root)
		if resolver != nil {
			namespaced, err := resolver.IsNamespaced(gvk)
			if err == nil && !namespaced {
				ns = "" // cluster-scoped objects have no namespace
			} else if namespaced && ns == "" {
				ns = fallbackNamespace
			}
		} else if ns == "" {
			ns = fallbackNamespace
		}

		applied = append(applied, AppliedObjectInfo{
			GroupKind: gk,
			Namespace: ns,
		})

		setMetadataLabel(root, applyset.PartOfLabel, id)
	}

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	for _, doc := range docs {
		if err := encoder.Encode(doc); err != nil {
			return "", nil, fmt.Errorf("failed to encode YAML: %w", err)
		}
	}

	return buf.String(), applied, nil
}

func setMetadataLabel(root *yaml.Node, key, value string) {
	var metaNode *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "metadata" && root.Content[i+1].Kind == yaml.MappingNode {
			metaNode = root.Content[i+1]
			break
		}
	}
	if metaNode == nil {
		kNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "metadata"}
		metaNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, kNode, metaNode)
	}

	var labelsNode *yaml.Node
	for i := 0; i+1 < len(metaNode.Content); i += 2 {
		if metaNode.Content[i].Value == "labels" && metaNode.Content[i+1].Kind == yaml.MappingNode {
			labelsNode = metaNode.Content[i+1]
			break
		}
	}
	if labelsNode == nil {
		kNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "labels"}
		labelsNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		metaNode.Content = append(metaNode.Content, kNode, labelsNode)
	}

	for i := 0; i+1 < len(labelsNode.Content); i += 2 {
		if labelsNode.Content[i].Value == key {
			labelsNode.Content[i+1].Value = value
			labelsNode.Content[i+1].Tag = "!!str"
			return
		}
	}
	labelsNode.Content = append(labelsNode.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	)
}

// VerifyApplySetParentTask verifies that the ApplySet parent exists,
// has matching ID (if already labeled), is not managed by another tool,
// and if custom resource, its CRD has the is-parent-type label.
type VerifyApplySetParentTask struct {
	Parent      *applyset.ParentRef
	KubeContext string
}

func (t *VerifyApplySetParentTask) Run(ctx context.Context, scope *tasks.TaskContext) error {
	kubectlBin, err := scope.Tool(ctx, "kubectl")
	if err != nil {
		return fmt.Errorf("failed to ensure kubectl: %w", err)
	}

	resourceArg := t.Parent.Kind
	if t.Parent.Resource != "" {
		resourceArg = t.Parent.Resource
	}
	if t.Parent.Group != "" {
		resourceArg = fmt.Sprintf("%s.%s", resourceArg, t.Parent.Group)
	}

	var args []string
	if t.KubeContext != "" {
		args = append(args, "--context", t.KubeContext)
	}
	if t.Parent.Namespace != "" {
		args = append(args, "-n", t.Parent.Namespace)
	}
	args = append(args, "get", resourceArg, t.Parent.Name, "-o", "json")

	cmd := exec.CommandContext(ctx, kubectlBin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(out))
		if trimmed != "" {
			return fmt.Errorf("applyset parent %s/%s does not exist: %s", resourceArg, t.Parent.Name, trimmed)
		}
		return fmt.Errorf("applyset parent %s/%s does not exist: %w", resourceArg, t.Parent.Name, err)
	}

	var parentObj struct {
		Metadata struct {
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(out, &parentObj); err != nil {
		return fmt.Errorf("failed to parse parent %s/%s JSON: %w", resourceArg, t.Parent.Name, err)
	}

	expectedID := t.Parent.ID()
	if id, ok := parentObj.Metadata.Labels[applyset.IDLabel]; ok && id != expectedID {
		return fmt.Errorf("applyset parent %s/%s has id %q, expected %q", resourceArg, t.Parent.Name, id, expectedID)
	}

	if tooling, ok := parentObj.Metadata.Annotations[applyset.ToolingAnnotation]; ok && tooling != "" {
		toolName, _, _ := strings.Cut(tooling, "/")
		if toolName != "ap" {
			return fmt.Errorf("applyset parent %s/%s is managed by tooling %q, not ap", resourceArg, t.Parent.Name, tooling)
		}
	}

	// Verify custom resource parent has CRD with is-parent-type label
	isCore := t.Parent.Group == "" && (t.Parent.Kind == "ConfigMap" || t.Parent.Kind == "Secret")
	if !isCore {
		crdName := fmt.Sprintf("%s.%s", t.Parent.Resource, t.Parent.Group)
		if t.Parent.Resource == "" {
			crdName = fmt.Sprintf("%ss.%s", strings.ToLower(t.Parent.Kind), t.Parent.Group)
		}
		var crdArgs []string
		if t.KubeContext != "" {
			crdArgs = append(crdArgs, "--context", t.KubeContext)
		}
		crdArgs = append(crdArgs, "get", "customresourcedefinition", crdName, "-o", "json")
		crdCmd := exec.CommandContext(ctx, kubectlBin, crdArgs...)
		crdOut, crdErr := crdCmd.CombinedOutput()
		if crdErr != nil {
			return fmt.Errorf("failed to get CRD %s for applyset parent: %w", crdName, crdErr)
		}
		var crdObj struct {
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(crdOut, &crdObj); err != nil {
			return fmt.Errorf("failed to parse CRD %s JSON: %w", crdName, err)
		}
		if crdObj.Metadata.Labels[applyset.IsParentTypeLabel] != "true" {
			return fmt.Errorf("parent CRD %s does not have label %s: true", crdName, applyset.IsParentTypeLabel)
		}
	}

	return nil
}

func (t *VerifyApplySetParentTask) GetName() string {
	res := t.Parent.Kind
	if t.Parent.Resource != "" {
		res = t.Parent.Resource
	}
	if t.Parent.Group != "" {
		res = fmt.Sprintf("%s.%s", res, t.Parent.Group)
	}
	if t.Parent.Namespace != "" {
		return fmt.Sprintf("applyset-verify-parent (%s/%s/%s)", res, t.Parent.Namespace, t.Parent.Name)
	}
	return fmt.Sprintf("applyset-verify-parent (%s/%s)", res, t.Parent.Name)
}

func (t *VerifyApplySetParentTask) GetChildren() []tasks.Task {
	return nil
}

// UpdateApplySetParentTask updates the parent object with ApplySet ID, tooling,
// and group-kinds/additional-namespaces metadata.
type UpdateApplySetParentTask struct {
	Parent      *applyset.ParentRef
	Membership  *applyset.Membership
	KubeContext string
}

func (t *UpdateApplySetParentTask) Run(ctx context.Context, scope *tasks.TaskContext) error {
	kubectlBin, err := scope.Tool(ctx, "kubectl")
	if err != nil {
		return fmt.Errorf("failed to ensure kubectl: %w", err)
	}

	tooling := fmt.Sprintf("ap/%s", version.Version())
	_, annotations := t.Membership.ParentMetadata(t.Parent.Namespace, tooling)

	resourceArg := t.Parent.Kind
	if t.Parent.Resource != "" {
		resourceArg = t.Parent.Resource
	}
	if t.Parent.Group != "" {
		resourceArg = fmt.Sprintf("%s.%s", resourceArg, t.Parent.Group)
	}

	// 1. Label parent with ID
	var labelArgs []string
	if t.KubeContext != "" {
		labelArgs = append(labelArgs, "--context", t.KubeContext)
	}
	if t.Parent.Namespace != "" {
		labelArgs = append(labelArgs, "-n", t.Parent.Namespace)
	}
	labelArgs = append(labelArgs, "label", "--overwrite", resourceArg, t.Parent.Name,
		fmt.Sprintf("%s=%s", applyset.IDLabel, t.Parent.ID()))

	labelCmd := exec.CommandContext(ctx, kubectlBin, labelArgs...)
	labelCmd.Stdout = scope.Stdout()
	labelCmd.Stderr = scope.Stderr()
	if err := labelCmd.Run(); err != nil {
		return fmt.Errorf("failed to label applyset parent %s/%s: %w", resourceArg, t.Parent.Name, err)
	}

	// 2. Annotate parent
	var annotateArgs []string
	if t.KubeContext != "" {
		annotateArgs = append(annotateArgs, "--context", t.KubeContext)
	}
	if t.Parent.Namespace != "" {
		annotateArgs = append(annotateArgs, "-n", t.Parent.Namespace)
	}
	annotateArgs = append(annotateArgs, "annotate", "--overwrite", resourceArg, t.Parent.Name)
	for k, v := range annotations {
		annotateArgs = append(annotateArgs, fmt.Sprintf("%s=%s", k, v))
	}
	// If additional-namespaces is omitted from annotations, remove it if it existed
	if _, ok := annotations[applyset.AdditionalNamespacesAnnotation]; !ok {
		annotateArgs = append(annotateArgs, fmt.Sprintf("%s-", applyset.AdditionalNamespacesAnnotation))
	}

	annotateCmd := exec.CommandContext(ctx, kubectlBin, annotateArgs...)
	annotateCmd.Stdout = scope.Stdout()
	annotateCmd.Stderr = scope.Stderr()
	if err := annotateCmd.Run(); err != nil {
		return fmt.Errorf("failed to annotate applyset parent %s/%s: %w", resourceArg, t.Parent.Name, err)
	}

	return nil
}

func (t *UpdateApplySetParentTask) GetName() string {
	res := t.Parent.Kind
	if t.Parent.Resource != "" {
		res = t.Parent.Resource
	}
	if t.Parent.Group != "" {
		res = fmt.Sprintf("%s.%s", res, t.Parent.Group)
	}
	if t.Parent.Namespace != "" {
		return fmt.Sprintf("applyset-update-parent (%s/%s/%s)", res, t.Parent.Namespace, t.Parent.Name)
	}
	return fmt.Sprintf("applyset-update-parent (%s/%s)", res, t.Parent.Name)
}

func (t *UpdateApplySetParentTask) GetChildren() []tasks.Task {
	return nil
}

// ResolveParentRef resolves the namespace and kind of parent using discovery.
// If the parent is namespaced and deployNamespace is empty, it returns the error:
// "namespace is required to use namespace-scoped ApplySet"
func ResolveParentRef(parent *applyset.ParentRef, deployNamespace string, kubeContext string, kubectlBin string) error {
	if parent == nil {
		return nil
	}

	var resolver ScopeResolver
	if kubectlBin != "" {
		r, err := GetDiscoveryScopeResolver(kubeContext, kubectlBin)
		if err != nil {
			klog.V(2).Infof("could not get discovery scope resolver: %v", err)
		} else {
			resolver = r
		}
	}

	// 1. Resolve kind from resource if needed
	if parent.Kind == "configmaps" || parent.Kind == "configmap" {
		parent.Kind = "ConfigMap"
		parent.Group = ""
	} else if parent.Kind == "secrets" || parent.Kind == "secret" {
		parent.Kind = "Secret"
		parent.Group = ""
	} else if resolver != nil {
		if kr, ok := resolver.(KindResolver); ok {
			if k, found := kr.KindForResource(parent.Resource, parent.Group); found {
				parent.Kind = k
			}
		}
	}

	// 2. Resolve namespace
	isNamespaced := true
	if resolver != nil {
		gvk := GroupVersionKind{Group: parent.Group, Kind: parent.Kind}
		ns, err := resolver.IsNamespaced(gvk)
		if err != nil {
			klog.V(2).Infof("could not determine scope for %s: %v", gvk.String(), err)
		} else if !ns {
			isNamespaced = false
		}
	}

	if isNamespaced {
		if deployNamespace == "" {
			return fmt.Errorf("namespace is required to use namespace-scoped ApplySet")
		}
		parent.Namespace = deployNamespace
	} else {
		parent.Namespace = ""
	}

	return nil
}
