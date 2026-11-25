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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/imageref"
	"gopkg.in/yaml.v3"
)

const contentHashLength = 10

type configResourceKey struct {
	kind      string
	namespace string
	name      string
}

// ConfigHashRename records a ConfigMap or Secret renamed with a content-hash suffix.
type ConfigHashRename struct {
	Kind         string
	Namespace    string
	OriginalName string
	SuffixedName string
}

// HashSuffixResult holds the rewritten YAML manifest and any ConfigMap/Secret renames applied.
type HashSuffixResult struct {
	Content string
	Renames []ConfigHashRename
}

// ConfigHashIndex indexes ConfigMap and Secret manifests across a deploy plan and
// maps their (kind, namespace, name) to deterministic content-hash-suffixed names.
type ConfigHashIndex struct {
	manifests []string
	once      sync.Once
	loadErr   error
	renames   map[configResourceKey]string
}

// NewConfigHashIndex creates a ConfigHashIndex that scans the given manifest paths on first use.
func NewConfigHashIndex(manifests []string) *ConfigHashIndex {
	return &ConfigHashIndex{
		manifests: manifests,
		renames:   make(map[configResourceKey]string),
	}
}

// EnsureLoaded reads and indexes all non-kustomization manifests in the index once,
// resolving image placeholders before computing content hashes.
func (index *ConfigHashIndex) EnsureLoaded(naming imageref.ImageNaming, targetNamespace string) error {
	index.once.Do(func() {
		for _, manifestPath := range index.manifests {
			if IsKustomization(manifestPath) {
				continue
			}
			rawContent, err := os.ReadFile(manifestPath)
			if err != nil {
				index.loadErr = fmt.Errorf("failed to read manifest %s: %w", manifestPath, err)
				return
			}
			replaced, err := replacePlaceholderImages(string(rawContent), naming)
			if err != nil {
				index.loadErr = fmt.Errorf("failed to replace placeholders in %s: %w", manifestPath, err)
				return
			}
			if err := index.AddContent(replaced, targetNamespace); err != nil {
				index.loadErr = fmt.Errorf("failed to index config hashes in %s: %w", manifestPath, err)
				return
			}
		}
	})
	return index.loadErr
}

// AddContent parses multi-document YAML content and records content-hash-suffixed names
// for every core v1 ConfigMap and Secret defined in it.
func (index *ConfigHashIndex) AddContent(content string, targetNamespace string) error {
	docs, err := decodeYAMLDocuments(content)
	if err != nil {
		return err
	}

	for _, doc := range docs {
		root := documentMappingRoot(doc)
		if root == nil {
			continue
		}
		apiVersion, kind := getAPIVersionAndKind(root)
		if !isCoreConfigResource(apiVersion, kind) {
			continue
		}
		name := getMetadataName(root)
		if name == "" {
			continue
		}
		hash, err := computeConfigResourceHash(root, kind, name)
		if err != nil {
			return fmt.Errorf("failed to hash %s/%s: %w", kind, name, err)
		}
		namespace := effectiveConfigNamespace(getMetadataNamespace(root), targetNamespace)
		key := configResourceKey{kind: kind, namespace: namespace, name: name}
		index.renames[key] = fmt.Sprintf("%s-%s", name, hash)
	}
	return nil
}

// RewriteManifest appends content-hash suffixes to indexed ConfigMap and Secret names
// and rewrites referencing workload fields to point to the suffixed names.
func (index *ConfigHashIndex) RewriteManifest(content string, targetNamespace string) (*HashSuffixResult, error) {
	if len(index.renames) == 0 {
		return &HashSuffixResult{Content: content}, nil
	}

	docs, err := decodeYAMLDocuments(content)
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return &HashSuffixResult{Content: content}, nil
	}

	modified := false
	var renames []ConfigHashRename

	for _, doc := range docs {
		root := documentMappingRoot(doc)
		if root == nil {
			continue
		}
		apiVersion, kind := getAPIVersionAndKind(root)
		if kind == "" {
			continue
		}
		namespace := effectiveConfigNamespace(getMetadataNamespace(root), targetNamespace)

		if isCoreConfigResource(apiVersion, kind) {
			nameNode := metadataNameNode(root)
			if nameNode == nil || nameNode.Value == "" {
				continue
			}
			key := configResourceKey{kind: kind, namespace: namespace, name: nameNode.Value}
			if suffixed, ok := index.renames[key]; ok {
				originalName := nameNode.Value
				nameNode.Value = suffixed
				nameNode.Tag = "!!str"
				modified = true
				renames = append(renames, ConfigHashRename{
					Kind:         kind,
					Namespace:    namespace,
					OriginalName: originalName,
					SuffixedName: suffixed,
				})
			}
			continue
		}

		podSpec := findPodSpecNode(root, apiVersion, kind)
		if podSpec != nil {
			if index.rewritePodSpecReferences(podSpec, namespace) {
				modified = true
			}
		}
	}

	if !modified {
		return &HashSuffixResult{Content: content}, nil
	}

	encoded, err := encodeYAMLDocuments(docs)
	if err != nil {
		return nil, err
	}
	return &HashSuffixResult{
		Content: encoded,
		Renames: renames,
	}, nil
}

func (index *ConfigHashIndex) rewritePodSpecReferences(podSpec *yaml.Node, namespace string) bool {
	modified := false

	for _, containerField := range []string{"containers", "initContainers", "ephemeralContainers"} {
		containers := resolveAlias(mappingValue(podSpec, containerField))
		if containers == nil || containers.Kind != yaml.SequenceNode {
			continue
		}
		for _, container := range containers.Content {
			container = resolveAlias(container)
			if container == nil || container.Kind != yaml.MappingNode {
				continue
			}
			if index.rewriteContainerEnvFrom(container, namespace) {
				modified = true
			}
			if index.rewriteContainerEnv(container, namespace) {
				modified = true
			}
		}
	}

	if index.rewriteVolumes(podSpec, namespace) {
		modified = true
	}
	if index.rewriteImagePullSecrets(podSpec, namespace) {
		modified = true
	}

	return modified
}

func (index *ConfigHashIndex) rewriteContainerEnvFrom(container *yaml.Node, namespace string) bool {
	envFrom := resolveAlias(mappingValue(container, "envFrom"))
	if envFrom == nil || envFrom.Kind != yaml.SequenceNode {
		return false
	}
	modified := false
	for _, entry := range envFrom.Content {
		entry = resolveAlias(entry)
		if entry == nil || entry.Kind != yaml.MappingNode {
			continue
		}
		if index.rewriteNamedRef(mappingValue(entry, "configMapRef"), "name", "ConfigMap", namespace) {
			modified = true
		}
		if index.rewriteNamedRef(mappingValue(entry, "secretRef"), "name", "Secret", namespace) {
			modified = true
		}
	}
	return modified
}

func (index *ConfigHashIndex) rewriteContainerEnv(container *yaml.Node, namespace string) bool {
	envVars := resolveAlias(mappingValue(container, "env"))
	if envVars == nil || envVars.Kind != yaml.SequenceNode {
		return false
	}
	modified := false
	for _, envVar := range envVars.Content {
		envVar = resolveAlias(envVar)
		if envVar == nil || envVar.Kind != yaml.MappingNode {
			continue
		}
		valueFrom := resolveAlias(mappingValue(envVar, "valueFrom"))
		if valueFrom == nil || valueFrom.Kind != yaml.MappingNode {
			continue
		}
		if index.rewriteNamedRef(mappingValue(valueFrom, "configMapKeyRef"), "name", "ConfigMap", namespace) {
			modified = true
		}
		if index.rewriteNamedRef(mappingValue(valueFrom, "secretKeyRef"), "name", "Secret", namespace) {
			modified = true
		}
	}
	return modified
}

func (index *ConfigHashIndex) rewriteVolumes(podSpec *yaml.Node, namespace string) bool {
	volumes := resolveAlias(mappingValue(podSpec, "volumes"))
	if volumes == nil || volumes.Kind != yaml.SequenceNode {
		return false
	}
	modified := false
	for _, volume := range volumes.Content {
		volume = resolveAlias(volume)
		if volume == nil || volume.Kind != yaml.MappingNode {
			continue
		}
		if index.rewriteNamedRef(mappingValue(volume, "configMap"), "name", "ConfigMap", namespace) {
			modified = true
		}
		if index.rewriteNamedRef(mappingValue(volume, "secret"), "secretName", "Secret", namespace) {
			modified = true
		}
		projected := resolveAlias(mappingValue(volume, "projected"))
		if projected != nil && projected.Kind == yaml.MappingNode {
			sources := resolveAlias(mappingValue(projected, "sources"))
			if sources != nil && sources.Kind == yaml.SequenceNode {
				for _, source := range sources.Content {
					source = resolveAlias(source)
					if source == nil || source.Kind != yaml.MappingNode {
						continue
					}
					if index.rewriteNamedRef(mappingValue(source, "configMap"), "name", "ConfigMap", namespace) {
						modified = true
					}
					if index.rewriteNamedRef(mappingValue(source, "secret"), "name", "Secret", namespace) {
						modified = true
					}
				}
			}
		}
	}
	return modified
}

func (index *ConfigHashIndex) rewriteImagePullSecrets(podSpec *yaml.Node, namespace string) bool {
	pullSecrets := resolveAlias(mappingValue(podSpec, "imagePullSecrets"))
	if pullSecrets == nil || pullSecrets.Kind != yaml.SequenceNode {
		return false
	}
	modified := false
	for _, entry := range pullSecrets.Content {
		if index.rewriteNamedRef(entry, "name", "Secret", namespace) {
			modified = true
		}
	}
	return modified
}

func (index *ConfigHashIndex) rewriteNamedRef(parent *yaml.Node, fieldName, kind, namespace string) bool {
	parent = resolveAlias(parent)
	if parent == nil || parent.Kind != yaml.MappingNode {
		return false
	}
	nameNode := resolveAlias(mappingValue(parent, fieldName))
	if nameNode == nil || nameNode.Kind != yaml.ScalarNode || nameNode.Value == "" {
		return false
	}
	key := configResourceKey{kind: kind, namespace: namespace, name: nameNode.Value}
	suffixed, ok := index.renames[key]
	if !ok {
		return false
	}
	nameNode.Value = suffixed
	nameNode.Tag = "!!str"
	return true
}

type configMapHashInput struct {
	Kind       string            `json:"kind"`
	Name       string            `json:"name"`
	Data       map[string]string `json:"data,omitempty"`
	BinaryData map[string]string `json:"binaryData,omitempty"`
}

type secretHashInput struct {
	Kind       string            `json:"kind"`
	Type       string            `json:"type,omitempty"`
	Name       string            `json:"name"`
	Data       map[string]string `json:"data,omitempty"`
	StringData map[string]string `json:"stringData,omitempty"`
}

func computeConfigResourceHash(root *yaml.Node, kind, name string) (string, error) {
	var payload []byte
	var err error

	switch kind {
	case "ConfigMap":
		input := configMapHashInput{
			Kind:       kind,
			Name:       name,
			Data:       extractStringMap(mappingValue(root, "data")),
			BinaryData: extractStringMap(mappingValue(root, "binaryData")),
		}
		payload, err = json.Marshal(input)
	case "Secret":
		secretType := ""
		if typeNode := resolveAlias(mappingValue(root, "type")); typeNode != nil && typeNode.Kind == yaml.ScalarNode {
			secretType = typeNode.Value
		}
		input := secretHashInput{
			Kind:       kind,
			Type:       secretType,
			Name:       name,
			Data:       extractStringMap(mappingValue(root, "data")),
			StringData: extractStringMap(mappingValue(root, "stringData")),
		}
		payload, err = json.Marshal(input)
	default:
		return "", fmt.Errorf("unsupported kind %q for content hashing", kind)
	}
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(payload)
	return fmt.Sprintf("%x", sum)[:contentHashLength], nil
}

func extractStringMap(node *yaml.Node) map[string]string {
	node = resolveAlias(node)
	if node == nil || node.Kind != yaml.MappingNode || len(node.Content) == 0 {
		return nil
	}
	values := make(map[string]string, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := resolveAlias(node.Content[i])
		valueNode := resolveAlias(node.Content[i+1])
		if keyNode == nil || valueNode == nil {
			continue
		}
		values[keyNode.Value] = valueNode.Value
	}
	return values
}

func isCoreConfigResource(apiVersion, kind string) bool {
	return apiVersion == "v1" && (kind == "ConfigMap" || kind == "Secret")
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

func findPodSpecNode(root *yaml.Node, apiVersion, kind string) *yaml.Node {
	switch kind {
	case "Pod":
		if apiVersion == "v1" {
			return resolveAlias(mappingValue(root, "spec"))
		}
	case "Deployment", "DaemonSet", "StatefulSet", "ReplicaSet":
		if strings.HasPrefix(apiVersion, "apps/") || strings.HasPrefix(apiVersion, "extensions/") {
			return mappingPath(root, "spec", "template", "spec")
		}
	case "ReplicationController":
		if apiVersion == "v1" {
			return mappingPath(root, "spec", "template", "spec")
		}
	case "Job":
		if strings.HasPrefix(apiVersion, "batch/") {
			return mappingPath(root, "spec", "template", "spec")
		}
	case "CronJob":
		if strings.HasPrefix(apiVersion, "batch/") {
			return mappingPath(root, "spec", "jobTemplate", "spec", "template", "spec")
		}
	}
	return nil
}

func metadataNameNode(root *yaml.Node) *yaml.Node {
	metadata := resolveAlias(mappingValue(root, "metadata"))
	if metadata == nil || metadata.Kind != yaml.MappingNode {
		return nil
	}
	nameNode := resolveAlias(mappingValue(metadata, "name"))
	if nameNode == nil || nameNode.Kind != yaml.ScalarNode {
		return nil
	}
	return nameNode
}

func mappingPath(root *yaml.Node, keys ...string) *yaml.Node {
	current := root
	for _, key := range keys {
		current = resolveAlias(mappingValue(current, key))
		if current == nil || current.Kind != yaml.MappingNode {
			return nil
		}
	}
	return current
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	node = resolveAlias(node)
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func resolveAlias(node *yaml.Node) *yaml.Node {
	if node != nil && node.Kind == yaml.AliasNode && node.Alias != nil {
		return node.Alias
	}
	return node
}

func documentMappingRoot(doc *yaml.Node) *yaml.Node {
	if doc == nil || doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil
	}
	root := resolveAlias(doc.Content[0])
	if root == nil || root.Kind != yaml.MappingNode {
		return nil
	}
	return root
}

func decodeYAMLDocuments(content string) ([]*yaml.Node, error) {
	decoder := yaml.NewDecoder(strings.NewReader(content))
	var docs []*yaml.Node
	for {
		var doc yaml.Node
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to decode YAML: %w", err)
		}
		docs = append(docs, &doc)
	}
	return docs, nil
}

func encodeYAMLDocuments(docs []*yaml.Node) (string, error) {
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	for _, doc := range docs {
		if err := encoder.Encode(doc); err != nil {
			return "", fmt.Errorf("failed to encode YAML: %w", err)
		}
	}
	if err := encoder.Close(); err != nil {
		return "", fmt.Errorf("failed to close YAML encoder: %w", err)
	}
	return buffer.String(), nil
}
