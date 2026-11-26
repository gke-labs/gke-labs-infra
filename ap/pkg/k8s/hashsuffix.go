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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/imageref"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"gopkg.in/yaml.v3"
)

const (
	// HashSuffixAnnotation opts a ConfigMap or Secret into content-hash name suffixing.
	HashSuffixAnnotation = "infra.labs.gke.io/hash-suffix"

	// HashSuffixOfLabel records the original unsuffixed name on a hash-suffixed ConfigMap or Secret.
	HashSuffixOfLabel = "infra.labs.gke.io/hash-suffix-of"

	contentHashLength = 10
)

type configHashKey struct {
	kind      string
	namespace string
	name      string
}

type configHashEntry struct {
	suffixedName      string
	explicitNamespace string
}

// ConfigHashMap maps (kind, namespace, name) of ConfigMaps and Secrets to their
// content-hash-suffixed names.
type ConfigHashMap map[configHashKey]configHashEntry

// SuffixedConfig identifies an annotated ConfigMap or Secret whose name was
// rewritten with a content-hash suffix.
type SuffixedConfig struct {
	Kind         string
	Namespace    string
	OriginalName string
	CurrentName  string
}

// SuffixedConfigs returns all entries in the map sorted deterministically by
// kind, namespace, and original name.
func (m ConfigHashMap) SuffixedConfigs() []SuffixedConfig {
	if len(m) == 0 {
		return nil
	}
	configs := make([]SuffixedConfig, 0, len(m))
	for key, entry := range m {
		configs = append(configs, SuffixedConfig{
			Kind:         key.kind,
			Namespace:    entry.explicitNamespace,
			OriginalName: key.name,
			CurrentName:  entry.suffixedName,
		})
	}
	sortSuffixedConfigs(configs)
	return configs
}

func sortSuffixedConfigs(configs []SuffixedConfig) {
	sort.Slice(configs, func(i, j int) bool {
		if configs[i].Kind != configs[j].Kind {
			return configs[i].Kind < configs[j].Kind
		}
		if configs[i].Namespace != configs[j].Namespace {
			return configs[i].Namespace < configs[j].Namespace
		}
		return configs[i].OriginalName < configs[j].OriginalName
	})
}

type configRename struct {
	kind string
	from string
	to   string
}

var podSpecRefFields = map[string]struct {
	kind   string
	fields []string
}{
	"configMapRef":    {kind: "ConfigMap", fields: []string{"name"}},
	"configMapKeyRef": {kind: "ConfigMap", fields: []string{"name"}},
	"configMap":       {kind: "ConfigMap", fields: []string{"name"}},
	"secretRef":       {kind: "Secret", fields: []string{"name"}},
	"secretKeyRef":    {kind: "Secret", fields: []string{"name"}},
	"secret":          {kind: "Secret", fields: []string{"secretName", "name"}},
}

// BuildConfigHashMap scans non-kustomization manifests and returns the map of
// annotated ConfigMap and Secret names to their content-hash-suffixed names.
func BuildConfigHashMap(manifests []string, naming imageref.ImageNaming, targetNamespace string) (ConfigHashMap, error) {
	renames := make(ConfigHashMap)
	for _, manifestPath := range manifests {
		if IsKustomization(manifestPath) {
			continue
		}
		rawContent, err := os.ReadFile(manifestPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read manifest %s: %w", manifestPath, err)
		}
		if !strings.Contains(string(rawContent), HashSuffixAnnotation) {
			continue
		}
		replaced, err := replacePlaceholderImages(string(rawContent), naming)
		if err != nil {
			return nil, fmt.Errorf("failed to replace placeholders in %s: %w", manifestPath, err)
		}
		if err := indexConfigHashes(renames, replaced, targetNamespace); err != nil {
			return nil, fmt.Errorf("%s: %w", manifestPath, err)
		}
	}
	return renames, nil
}

// indexConfigHashes records suffixed names in renames for every core v1 ConfigMap
// or Secret in content annotated with HashSuffixAnnotation: "true".
func indexConfigHashes(renames ConfigHashMap, content, targetNamespace string) error {
	docs, err := decodeYAMLDocuments(content)
	if err != nil {
		return err
	}
	for _, doc := range docs {
		if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
			continue
		}
		root := doc.Content[0]
		apiVersion, kind := getAPIVersionAndKind(root)
		if apiVersion != "v1" || (kind != "ConfigMap" && kind != "Secret") {
			continue
		}
		name := getMetadataName(root)
		if name == "" {
			continue
		}
		annNode := yamlPath(root, "metadata", "annotations", HashSuffixAnnotation)
		if annNode == nil {
			continue
		}
		if annNode.Kind != yaml.ScalarNode || annNode.Tag != "!!str" || annNode.Value != "true" {
			return fmt.Errorf("%s/%s: invalid %s annotation %q (only \"true\" is supported)", kind, name, HashSuffixAnnotation, annNode.Value)
		}
		hash, err := computeConfigHash(root, kind, name)
		if err != nil {
			return fmt.Errorf("failed to hash %s/%s: %w", kind, name, err)
		}
		documentNamespace := getMetadataNamespace(root)
		namespace := effectiveConfigNamespace(documentNamespace, targetNamespace)
		explicitNamespace := targetNamespace
		if explicitNamespace == "" {
			explicitNamespace = documentNamespace
		}
		renames[configHashKey{kind: kind, namespace: namespace, name: name}] = configHashEntry{
			suffixedName:      name + "-" + hash,
			explicitNamespace: explicitNamespace,
		}
	}
	return nil
}

// rewriteConfigHashes renames ConfigMaps and Secrets present in renames, records
// HashSuffixOfLabel on each renamed object, and rewrites references to them in
// pod specs of supported workloads in the same namespace.
// Objects not in renames and references from other resource kinds are left untouched.
// If nothing in content matched, content is returned unchanged without re-encoding.
func rewriteConfigHashes(content, targetNamespace string, renames ConfigHashMap) (string, []configRename, error) {
	if len(renames) == 0 {
		return content, nil, nil
	}
	docs, err := decodeYAMLDocuments(content)
	if err != nil || len(docs) == 0 {
		return content, nil, err
	}

	modified := false
	var applied []configRename
	for _, doc := range docs {
		if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
			continue
		}
		root := doc.Content[0]
		apiVersion, kind := getAPIVersionAndKind(root)
		ns := effectiveConfigNamespace(getMetadataNamespace(root), targetNamespace)

		if apiVersion == "v1" && (kind == "ConfigMap" || kind == "Secret") {
			if from, to, ok := rewriteRefField(yamlPath(root, "metadata"), "name", kind, ns, renames); ok {
				setMetadataLabel(root, HashSuffixOfLabel, from)
				applied = append(applied, configRename{kind: kind, from: from, to: to})
				modified = true
			}
			continue
		}
		if podSpec := findPodSpecNode(root, apiVersion, kind); podSpec != nil && rewritePodSpecRefs(podSpec, renames, ns) {
			modified = true
		}
	}

	if !modified {
		return content, nil, nil
	}
	encoded, err := encodeYAMLDocuments(docs)
	return encoded, applied, err
}

func rewritePodSpecRefs(node *yaml.Node, renames ConfigHashMap, namespace string) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		node = node.Alias
	}
	modified := false
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, val := node.Content[i].Value, node.Content[i+1]
			if target, ok := podSpecRefFields[key]; ok {
				for _, field := range target.fields {
					if _, _, changed := rewriteRefField(val, field, target.kind, namespace, renames); changed {
						modified = true
					}
				}
				continue
			}
			if key == "imagePullSecrets" && val.Kind == yaml.SequenceNode {
				for _, item := range val.Content {
					if _, _, changed := rewriteRefField(item, "name", "Secret", namespace, renames); changed {
						modified = true
					}
				}
				continue
			}
			if rewritePodSpecRefs(val, renames, namespace) {
				modified = true
			}
		}
	case yaml.SequenceNode:
		for _, item := range node.Content {
			if rewritePodSpecRefs(item, renames, namespace) {
				modified = true
			}
		}
	}
	return modified
}

func rewriteRefField(parent *yaml.Node, field, kind, namespace string, renames ConfigHashMap) (string, string, bool) {
	nameNode := yamlPath(parent, field)
	if nameNode == nil || nameNode.Kind != yaml.ScalarNode || nameNode.Value == "" {
		return "", "", false
	}
	from := nameNode.Value
	entry, ok := renames[configHashKey{kind: kind, namespace: namespace, name: from}]
	if !ok {
		return "", "", false
	}
	nameNode.Value = entry.suffixedName
	nameNode.Tag = "!!str"
	return from, entry.suffixedName, true
}

// computeConfigHash hashes a canonical JSON encoding of kind, name, type, data,
// binaryData, and stringData into a 10-character lowercase hex SHA-256 prefix.
// Labels, annotations, map key order, and YAML comments are excluded so only
// content or type changes rotate the suffixed name.
func computeConfigHash(root *yaml.Node, kind, name string) (string, error) {
	var obj struct {
		Kind       string            `yaml:"-" json:"kind"`
		Name       string            `yaml:"-" json:"name"`
		Type       string            `yaml:"type" json:"type,omitempty"`
		Data       map[string]string `yaml:"data" json:"data,omitempty"`
		BinaryData map[string]string `yaml:"binaryData" json:"binaryData,omitempty"`
		StringData map[string]string `yaml:"stringData" json:"stringData,omitempty"`
	}
	if err := root.Decode(&obj); err != nil {
		return "", err
	}
	obj.Kind, obj.Name = kind, name
	payload, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("%x", sum)[:contentHashLength], nil
}

func effectiveConfigNamespace(docNamespace, targetNamespace string) string {
	if targetNamespace != "" {
		return targetNamespace
	}
	if docNamespace != "" {
		return docNamespace
	}
	return "default"
}

// findPodSpecNode returns the pod spec YAML node for Pod, Deployment, DaemonSet,
// StatefulSet, Job, and CronJob manifests, or nil for all other kinds.
func findPodSpecNode(root *yaml.Node, apiVersion, kind string) *yaml.Node {
	switch kind {
	case "Pod":
		if apiVersion == "v1" {
			return yamlPath(root, "spec")
		}
	case "Deployment", "DaemonSet", "StatefulSet":
		if strings.HasPrefix(apiVersion, "apps/") {
			return yamlPath(root, "spec", "template", "spec")
		}
	case "Job":
		if strings.HasPrefix(apiVersion, "batch/") {
			return yamlPath(root, "spec", "template", "spec")
		}
	case "CronJob":
		if strings.HasPrefix(apiVersion, "batch/") {
			return yamlPath(root, "spec", "jobTemplate", "spec", "template", "spec")
		}
	}
	return nil
}

func yamlPath(node *yaml.Node, keys ...string) *yaml.Node {
	for _, key := range keys {
		if node != nil && node.Kind == yaml.AliasNode && node.Alias != nil {
			node = node.Alias
		}
		if node == nil || node.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				next = node.Content[i+1]
				break
			}
		}
		node = next
	}
	if node != nil && node.Kind == yaml.AliasNode && node.Alias != nil {
		return node.Alias
	}
	return node
}

type labeledObject struct {
	Metadata struct {
		Name      string            `json:"name"`
		Namespace string            `json:"namespace"`
		Labels    map[string]string `json:"labels"`
	} `json:"metadata"`
}

type labeledObjectList struct {
	Items []labeledObject `json:"items"`
}

type configObject struct {
	Name      string
	Namespace string
}

// selectSupersededConfigs returns the objects in items that carry
// HashSuffixOfLabel equal to originalName and whose name differs from
// currentName, sorted by name.
func selectSupersededConfigs(items []labeledObject, originalName, currentName string) []configObject {
	if originalName == "" {
		return nil
	}
	var superseded []configObject
	for _, item := range items {
		if item.Metadata.Name == "" || item.Metadata.Name == currentName {
			continue
		}
		if item.Metadata.Labels[HashSuffixOfLabel] != originalName {
			continue
		}
		superseded = append(superseded, configObject{
			Name:      item.Metadata.Name,
			Namespace: item.Metadata.Namespace,
		})
	}
	sort.Slice(superseded, func(i, j int) bool {
		return superseded[i].Name < superseded[j].Name
	})
	return superseded
}

// RemoveSupersededConfigTask deletes older hash-suffixed ConfigMaps or Secrets
// carrying HashSuffixOfLabel for a single original name while keeping CurrentName.
type RemoveSupersededConfigTask struct {
	Config  SuffixedConfig
	Cluster KubeCluster
}

// GetName returns the display name for the cleanup task.
func (t *RemoveSupersededConfigTask) GetName() string {
	target := fmt.Sprintf("%s/%s", t.Config.Kind, t.Config.OriginalName)
	var parts []string
	if cluster := t.Cluster.String(); cluster != "" {
		parts = append(parts, "context: "+cluster)
	}
	if t.Config.Namespace != "" {
		parts = append(parts, "namespace: "+t.Config.Namespace)
	}
	parts = append(parts, "keep: "+t.Config.CurrentName)
	return fmt.Sprintf("remove-superseded-%s (%s)", target, strings.Join(parts, ", "))
}

// GetChildren returns nil because RemoveSupersededConfigTask is a leaf task.
func (t *RemoveSupersededConfigTask) GetChildren() []tasks.Task {
	return nil
}

// Run lists matching labeled objects in the target namespace and deletes all
// superseded ones whose name differs from CurrentName.
func (t *RemoveSupersededConfigTask) Run(ctx context.Context, scope *tasks.TaskContext) error {
	kubectlBinary, err := scope.Tool(ctx, "kubectl")
	if err != nil {
		return fmt.Errorf("failed to ensure kubectl: %w", err)
	}

	resource := strings.ToLower(t.Config.Kind)
	selector := fmt.Sprintf("%s=%s", HashSuffixOfLabel, t.Config.OriginalName)

	var getArgs []string
	getArgs = append(getArgs, t.Cluster.KubectlArgs()...)
	if t.Config.Namespace != "" {
		getArgs = append(getArgs, "-n", t.Config.Namespace)
	}
	getArgs = append(getArgs, "get", resource, "-l", selector, "-o", "json")

	getCommand := exec.CommandContext(ctx, kubectlBinary, getArgs...)
	var stdout, stderr bytes.Buffer
	getCommand.Stdout = &stdout
	getCommand.Stderr = &stderr
	if err := getCommand.Run(); err != nil {
		errorMessage := strings.TrimSpace(stderr.String())
		if errorMessage != "" {
			return fmt.Errorf("failed to list %s with %s: %w: %s", resource, selector, err, errorMessage)
		}
		return fmt.Errorf("failed to list %s with %s: %w", resource, selector, err)
	}

	var list labeledObjectList
	if err := json.Unmarshal(stdout.Bytes(), &list); err != nil {
		return fmt.Errorf("failed to parse %s list JSON: %w", resource, err)
	}

	for _, object := range selectSupersededConfigs(list.Items, t.Config.OriginalName, t.Config.CurrentName) {
		namespace := t.Config.Namespace
		if namespace == "" {
			namespace = object.Namespace
		}
		var deleteArgs []string
		deleteArgs = append(deleteArgs, t.Cluster.KubectlArgs()...)
		if namespace != "" {
			deleteArgs = append(deleteArgs, "-n", namespace)
		}
		deleteArgs = append(deleteArgs, "delete", resource, object.Name, "--ignore-not-found=true")

		deleteCommand := exec.CommandContext(ctx, kubectlBinary, deleteArgs...)
		var deleteStderr bytes.Buffer
		deleteCommand.Stderr = &deleteStderr
		if err := deleteCommand.Run(); err != nil {
			errorMessage := strings.TrimSpace(deleteStderr.String())
			if errorMessage != "" {
				return fmt.Errorf("failed to delete %s/%s: %w: %s", t.Config.Kind, object.Name, err, errorMessage)
			}
			return fmt.Errorf("failed to delete %s/%s: %w", t.Config.Kind, object.Name, err)
		}
		scoped.Log(ctx).Info("removed superseded config", "kind", t.Config.Kind, "namespace", namespace, "name", object.Name)
	}
	return nil
}

// RemoveSupersededConfigsTasks returns a task group that deletes superseded
// hash-suffixed ConfigMaps and Secrets for each config in configs.
func RemoveSupersededConfigsTasks(configs []SuffixedConfig, cluster KubeCluster) tasks.Task {
	sorted := append([]SuffixedConfig(nil), configs...)
	sortSuffixedConfigs(sorted)
	subTasks := make([]tasks.Task, 0, len(sorted))
	for _, config := range sorted {
		subTasks = append(subTasks, &RemoveSupersededConfigTask{
			Config:  config,
			Cluster: cluster,
		})
	}
	return &tasks.Group{
		Name:  "remove-superseded-configs",
		Tasks: subTasks,
	}
}
