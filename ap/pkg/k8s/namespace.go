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
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// ValidateNamespaceName validates that a namespace name is valid according to Kubernetes DNS-1123 label rules:
// - At most 63 characters
// - Lowercase alphanumeric characters ('a'-'z', '0'-'9') or '-'
// - Must start and end with an alphanumeric character ('a'-'z', '0'-'9')
func ValidateNamespaceName(ns string) error {
	if ns == "" {
		return nil
	}
	if len(ns) > 63 {
		return fmt.Errorf("namespace %q is invalid: must be no more than 63 characters", ns)
	}
	for i := 0; i < len(ns); i++ {
		c := ns[i]
		isAlphaNum := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if i == 0 || i == len(ns)-1 {
			if !isAlphaNum {
				return fmt.Errorf("namespace %q is invalid: must consist of lower case alphanumeric characters or '-', and must start and end with an alphanumeric character", ns)
			}
		} else {
			if !isAlphaNum && c != '-' {
				return fmt.Errorf("namespace %q is invalid: must consist of lower case alphanumeric characters or '-', and must start and end with an alphanumeric character", ns)
			}
		}
	}
	return nil
}

// GroupVersionKind represents the group, version, and kind of a Kubernetes object.
type GroupVersionKind struct {
	Group   string
	Version string
	Kind    string
}

func (gvk GroupVersionKind) String() string {
	if gvk.Group == "" {
		return fmt.Sprintf("%s, Kind=%s", gvk.Version, gvk.Kind)
	}
	return fmt.Sprintf("%s/%s, Kind=%s", gvk.Group, gvk.Version, gvk.Kind)
}

// GroupKind represents the group and kind of a Kubernetes object.
type GroupKind struct {
	Group string
	Kind  string
}

// ScopeResolver determines whether a Kubernetes GroupVersionKind is namespaced or cluster-scoped.
type ScopeResolver interface {
	IsNamespaced(gvk GroupVersionKind) (bool, error)
}

// KubectlScopeResolver implements ScopeResolver by querying discovery via "kubectl api-resources".
type KubectlScopeResolver struct {
	mu          sync.Mutex
	loaded      bool
	namespaced  map[GroupKind]bool
	byKind      map[string]bool
	execCmd     func(ctx context.Context, name string, args ...string) ([]byte, error)
	KubeContext string
}

// NewKubectlScopeResolver creates a new KubectlScopeResolver.
func NewKubectlScopeResolver() *KubectlScopeResolver {
	return &KubectlScopeResolver{
		namespaced: make(map[GroupKind]bool),
		byKind:     make(map[string]bool),
	}
}

// IsNamespaced implements ScopeResolver.
func (r *KubectlScopeResolver) IsNamespaced(gvk GroupVersionKind) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.loaded {
		if err := r.load(context.Background()); err != nil {
			return false, err
		}
	}

	if ns, ok := r.namespaced[GroupKind{Group: gvk.Group, Kind: gvk.Kind}]; ok {
		return ns, nil
	}
	if ns, ok := r.byKind[gvk.Kind]; ok {
		return ns, nil
	}

	return false, fmt.Errorf("unknown scope for %s", gvk.String())
}

func (r *KubectlScopeResolver) load(ctx context.Context) error {
	var out []byte
	var err error
	var args []string
	if r.KubeContext != "" {
		args = append(args, "--context", r.KubeContext)
	}
	args = append(args, "api-resources")
	if r.execCmd != nil {
		out, err = r.execCmd(ctx, "kubectl", args...)
	} else {
		cmd := exec.CommandContext(ctx, "kubectl", args...)
		out, err = cmd.Output()
	}
	if err != nil {
		return fmt.Errorf("failed to run kubectl api-resources: %w", err)
	}

	return r.parseOutput(string(out))
}

func (r *KubectlScopeResolver) parseOutput(output string) error {
	lines := strings.Split(output, "\n")
	if len(lines) == 0 {
		return fmt.Errorf("empty output from kubectl api-resources")
	}

	header := lines[0]
	colNamespaced := strings.Index(header, "NAMESPACED")
	colKind := strings.Index(header, "KIND")
	colAPIVersion := strings.Index(header, "APIVERSION")
	if colAPIVersion == -1 {
		colAPIVersion = strings.Index(header, "APIGROUP")
	}

	useColumns := colNamespaced != -1 && colKind != -1 && colAPIVersion != -1

	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		var apiVerOrGroup, namespacedStr, kind string

		if useColumns && len(line) >= colKind {
			kind = strings.TrimSpace(line[colKind:])
			if fields := strings.Fields(kind); len(fields) > 0 {
				kind = fields[0]
			}
			namespacedStr = strings.TrimSpace(line[colNamespaced:colKind])
			if colAPIVersion < colNamespaced {
				apiVerOrGroup = strings.TrimSpace(line[colAPIVersion:colNamespaced])
			}
		} else {
			fields := strings.Fields(trimmed)
			nsIdx := -1
			for i, f := range fields {
				if f == "true" || f == "false" {
					nsIdx = i
					break
				}
			}
			if nsIdx > 0 && nsIdx+1 < len(fields) {
				namespacedStr = fields[nsIdx]
				kind = fields[nsIdx+1]
				apiVerOrGroup = fields[nsIdx-1]
			}
		}

		if kind == "" || namespacedStr == "" {
			continue
		}

		namespaced := (namespacedStr == "true")
		group := ""
		if strings.Contains(apiVerOrGroup, "/") {
			group = strings.SplitN(apiVerOrGroup, "/", 2)[0]
		} else if apiVerOrGroup != "v1" {
			group = apiVerOrGroup
		}

		r.namespaced[GroupKind{Group: group, Kind: kind}] = namespaced
		r.byKind[kind] = namespaced
	}

	r.loaded = true
	return nil
}

// NewDiscoveryScopeResolver creates a ScopeResolver using discovery from "kubectl api-resources".
func NewDiscoveryScopeResolver() (ScopeResolver, error) {
	return NewKubectlScopeResolver(), nil
}

// MapScopeResolver implements ScopeResolver using a simple map for testing or static resolution.
type MapScopeResolver map[GroupVersionKind]bool

var (
	discoveryResolverMu sync.Mutex
	cachedDiscoveryMap  = make(map[string]ScopeResolver)

	verifiedNamespacesMu sync.Mutex
	verifiedNamespaces   = make(map[string]bool)
)

// GetDiscoveryScopeResolver returns a cached or newly initialized discovery ScopeResolver.
func GetDiscoveryScopeResolver(kubeContext ...string) (ScopeResolver, error) {
	kCtx := ""
	if len(kubeContext) > 0 {
		kCtx = kubeContext[0]
	}
	discoveryResolverMu.Lock()
	defer discoveryResolverMu.Unlock()
	if r, ok := cachedDiscoveryMap[kCtx]; ok {
		return r, nil
	}
	resolver := NewKubectlScopeResolver()
	resolver.KubeContext = kCtx
	cachedDiscoveryMap[kCtx] = resolver
	return resolver, nil
}

// ResetDiscoveryScopeResolver resets the cached discovery scope resolver (primarily for testing).
func ResetDiscoveryScopeResolver() {
	discoveryResolverMu.Lock()
	defer discoveryResolverMu.Unlock()
	cachedDiscoveryMap = make(map[string]ScopeResolver)
}

// DefaultCheckNamespaceCached checks if the target namespace exists in the cluster, caching successful checks.
func DefaultCheckNamespaceCached(ctx context.Context, namespace string, kubeContext ...string) error {
	kCtx := ""
	if len(kubeContext) > 0 {
		kCtx = kubeContext[0]
	}
	cacheKey := namespace
	if kCtx != "" {
		cacheKey = kCtx + "/" + namespace
	}
	verifiedNamespacesMu.Lock()
	verified := verifiedNamespaces[cacheKey]
	verifiedNamespacesMu.Unlock()
	if verified {
		return nil
	}

	if err := CheckNamespaceExists(ctx, namespace, kCtx); err != nil {
		return err
	}

	verifiedNamespacesMu.Lock()
	verifiedNamespaces[cacheKey] = true
	verifiedNamespacesMu.Unlock()
	return nil
}

// IsNamespaced implements ScopeResolver.
func (m MapScopeResolver) IsNamespaced(gvk GroupVersionKind) (bool, error) {
	if namespaced, ok := m[gvk]; ok {
		return namespaced, nil
	}
	for k, v := range m {
		if k.Group == gvk.Group && k.Kind == gvk.Kind {
			return v, nil
		}
	}
	for k, v := range m {
		if k.Kind == gvk.Kind {
			return v, nil
		}
	}
	return false, fmt.Errorf("unknown scope for GVK: %v", gvk)
}

// CheckNamespaceExistsFn is the function used to check namespace existence.
var (
	isCustomCheckNamespaceFn bool
	CheckNamespaceExistsFn   func(ctx context.Context, namespace string) error = func(ctx context.Context, namespace string) error {
		return checkNamespaceExistsDefault(ctx, namespace)
	}
)

// SetCheckNamespaceExistsForTest overrides CheckNamespaceExistsFn for tests and returns a restore function.
func SetCheckNamespaceExistsForTest(fn func(ctx context.Context, namespace string) error) func() {
	prev := CheckNamespaceExistsFn
	prevCustom := isCustomCheckNamespaceFn
	CheckNamespaceExistsFn = fn
	isCustomCheckNamespaceFn = (fn != nil)
	return func() {
		CheckNamespaceExistsFn = prev
		isCustomCheckNamespaceFn = prevCustom
	}
}

// CheckNamespaceExists checks whether the target namespace exists in the cluster.
// It fails if the namespace does not exist, without creating it implicitly.
func CheckNamespaceExists(ctx context.Context, namespace string, kubeContext ...string) error {
	if isCustomCheckNamespaceFn && CheckNamespaceExistsFn != nil {
		return CheckNamespaceExistsFn(ctx, namespace)
	}
	kCtx := ""
	if len(kubeContext) > 0 {
		kCtx = kubeContext[0]
	}
	return checkNamespaceExistsDefault(ctx, namespace, kCtx)
}

func checkNamespaceExistsDefault(ctx context.Context, namespace string, kubeContext ...string) error {
	kCtx := ""
	if len(kubeContext) > 0 {
		kCtx = kubeContext[0]
	}
	var args []string
	if kCtx != "" {
		args = append(args, "--context", kCtx)
	}
	args = append(args, "get", "namespace", namespace, "-o", "name")
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		trimmed := strings.TrimSpace(string(out))
		if trimmed != "" {
			return fmt.Errorf("target namespace %q does not exist: %s", namespace, trimmed)
		}
		return fmt.Errorf("target namespace %q does not exist: %w", namespace, err)
	}
	return nil
}

// RetargetManifestNamespace rewrites namespaced objects in content to targetNamespace,
// rewrites RoleBinding and ClusterRoleBinding ServiceAccount subjects whose namespace matches originalNamespace,
// leaves cluster-scoped objects unchanged, and returns the modified YAML along with a slice of cluster-scoped objects.
func RetargetManifestNamespace(content string, targetNamespace string, resolver ScopeResolver, originalNamespace string) (string, []string, error) {
	if targetNamespace == "" {
		return content, nil, nil
	}

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

	// If originalNamespace is not provided, detect it from namespaced objects in the manifest.
	if originalNamespace == "" {
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
			namespaced, err := resolver.IsNamespaced(gvk)
			if err == nil && namespaced {
				ns := getMetadataNamespace(root)
				if ns != "" {
					originalNamespace = ns
					break
				}
			}
		}
		// If still empty, fall back to "default"
		if originalNamespace == "" {
			originalNamespace = "default"
		}
	}

	var clusterScopedObjects []string

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
		namespaced, err := resolver.IsNamespaced(gvk)
		if err != nil {
			return "", nil, fmt.Errorf("failed to determine scope for %s: %w", gvk.String(), err)
		}

		name := getMetadataName(root)
		docOriginalNamespace := getMetadataNamespace(root)

		if namespaced {
			setMetadataNamespace(root, targetNamespace)
		} else {
			objDesc := kind
			if name != "" {
				objDesc = fmt.Sprintf("%s/%s", kind, name)
			}
			clusterScopedObjects = append(clusterScopedObjects, objDesc)
		}

		// In RoleBinding and ClusterRoleBinding, rewrite ServiceAccount subjects whose namespace matches originalNamespace
		if kind == "RoleBinding" || kind == "ClusterRoleBinding" {
			rewriteServiceAccountSubjects(root, targetNamespace, originalNamespace, docOriginalNamespace)
		}
	}

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	for _, doc := range docs {
		if err := encoder.Encode(doc); err != nil {
			return "", nil, fmt.Errorf("failed to encode YAML: %w", err)
		}
	}
	if err := encoder.Close(); err != nil {
		return "", nil, fmt.Errorf("failed to close YAML encoder: %w", err)
	}

	return buf.String(), clusterScopedObjects, nil
}

func getAPIVersionAndKind(root *yaml.Node) (string, string) {
	var apiVersion, kind string
	for i := 0; i+1 < len(root.Content); i += 2 {
		k := root.Content[i].Value
		v := root.Content[i+1].Value
		if k == "apiVersion" {
			apiVersion = v
		} else if k == "kind" {
			kind = v
		}
	}
	return apiVersion, kind
}

func parseGVK(apiVersion, kind string) GroupVersionKind {
	parts := strings.SplitN(apiVersion, "/", 2)
	if len(parts) == 2 {
		return GroupVersionKind{Group: parts[0], Version: parts[1], Kind: kind}
	}
	return GroupVersionKind{Group: "", Version: apiVersion, Kind: kind}
}

func getMetadataName(root *yaml.Node) string {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "metadata" && root.Content[i+1].Kind == yaml.MappingNode {
			metaNode := root.Content[i+1]
			for j := 0; j+1 < len(metaNode.Content); j += 2 {
				if metaNode.Content[j].Value == "name" {
					return metaNode.Content[j+1].Value
				}
			}
		}
	}
	return ""
}

func getMetadataNamespace(root *yaml.Node) string {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "metadata" && root.Content[i+1].Kind == yaml.MappingNode {
			metaNode := root.Content[i+1]
			for j := 0; j+1 < len(metaNode.Content); j += 2 {
				if metaNode.Content[j].Value == "namespace" {
					return metaNode.Content[j+1].Value
				}
			}
		}
	}
	return ""
}

func setMetadataNamespace(root *yaml.Node, targetNamespace string) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "metadata" {
			if root.Content[i+1].Kind == yaml.MappingNode {
				metaNode := root.Content[i+1]
				for j := 0; j+1 < len(metaNode.Content); j += 2 {
					if metaNode.Content[j].Value == "namespace" {
						metaNode.Content[j+1].Value = targetNamespace
						metaNode.Content[j+1].Tag = "!!str"
						return
					}
				}
				keyNode := &yaml.Node{
					Kind:  yaml.ScalarNode,
					Tag:   "!!str",
					Value: "namespace",
				}
				valNode := &yaml.Node{
					Kind:  yaml.ScalarNode,
					Tag:   "!!str",
					Value: targetNamespace,
				}
				metaNode.Content = append(metaNode.Content, keyNode, valNode)
				return
			}
		}
	}

	metaKey := &yaml.Node{
		Kind:  yaml.ScalarNode,
		Tag:   "!!str",
		Value: "metadata",
	}
	metaVal := &yaml.Node{
		Kind: yaml.MappingNode,
		Tag:  "!!map",
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "namespace"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: targetNamespace},
		},
	}
	root.Content = append(root.Content, metaKey, metaVal)
}

func rewriteServiceAccountSubjects(root *yaml.Node, targetNamespace, originalNamespace, docOriginalNamespace string) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "subjects" && root.Content[i+1].Kind == yaml.SequenceNode {
			subjectsSeq := root.Content[i+1]
			for _, item := range subjectsSeq.Content {
				if item.Kind != yaml.MappingNode {
					continue
				}
				var subKind string
				var nsNode *yaml.Node
				for j := 0; j+1 < len(item.Content); j += 2 {
					k := item.Content[j].Value
					if k == "kind" {
						subKind = item.Content[j+1].Value
					} else if k == "namespace" {
						nsNode = item.Content[j+1]
					}
				}

				if subKind == "ServiceAccount" && nsNode != nil {
					match := false
					if originalNamespace != "" && nsNode.Value == originalNamespace {
						match = true
					}
					if docOriginalNamespace != "" && nsNode.Value == docOriginalNamespace {
						match = true
					}
					if match {
						nsNode.Value = targetNamespace
						nsNode.Tag = "!!str"
					}
				}
			}
		}
	}
}

// FindOriginalNamespaceInFiles scans manifest files to find any explicit metadata.namespace on namespaced objects.
func FindOriginalNamespaceInFiles(manifests []string, resolver ScopeResolver) string {
	for _, m := range manifests {
		ns, err := findOriginalNamespaceInManifestFile(m, resolver)
		if err == nil && ns != "" {
			return ns
		}
	}
	return ""
}

func findOriginalNamespaceInManifestFile(path string, resolver ScopeResolver) (string, error) {
	content, err := osReadFile(path)
	if err != nil {
		return "", err
	}

	decoder := yaml.NewDecoder(bytes.NewReader(content))
	for {
		var doc yaml.Node
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
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
		if resolver != nil {
			namespaced, err := resolver.IsNamespaced(gvk)
			if err == nil && !namespaced {
				continue
			}
		}
		ns := getMetadataNamespace(root)
		if ns != "" {
			return ns, nil
		}
	}
	return "", nil
}

var osReadFile = func(path string) ([]byte, error) {
	return os.ReadFile(path)
}
