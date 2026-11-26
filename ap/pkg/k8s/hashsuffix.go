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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/imageref"
	"gopkg.in/yaml.v3"
)

const (
	// HashSuffixAnnotation opts a ConfigMap or Secret into content-hash name suffixing.
	HashSuffixAnnotation = "infra.labs.gke.io/hash-suffix"

	contentHashLength = 10
)

type configHashKey struct {
	kind      string
	namespace string
	name      string
}

// ConfigHashMap maps (kind, namespace, name) of ConfigMaps and Secrets to their
// content-hash-suffixed names.
type ConfigHashMap map[configHashKey]string

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
		ns := effectiveConfigNamespace(getMetadataNamespace(root), targetNamespace)
		renames[configHashKey{kind: kind, namespace: ns, name: name}] = name + "-" + hash
	}
	return nil
}

// rewriteConfigHashes renames ConfigMaps and Secrets present in renames and rewrites
// references to them in pod specs of supported workloads in the same namespace.
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
	to, ok := renames[configHashKey{kind: kind, namespace: namespace, name: from}]
	if !ok {
		return "", "", false
	}
	nameNode.Value = to
	nameNode.Tag = "!!str"
	return from, to, true
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
