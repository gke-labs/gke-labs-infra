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
	"path/filepath"
	"sort"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/imageref"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/repo"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"
)

type imageReferenceMatch struct {
	start     int
	end       int
	imageName string
}

const (
	imagePrefixPlaceholder = "${IMAGE_PREFIX}/"
	imageTagPlaceholder    = ":${IMAGE_TAG}"
)

func isAlphaNumeric(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func isImageNameChar(b byte) bool {
	return isAlphaNumeric(b) || b == '.' || b == '_' || b == '-' || b == '/'
}

func findImageReferenceMatches(s string) []imageReferenceMatch {
	var matches []imageReferenceMatch
	offset := 0
	for {
		idx := strings.Index(s[offset:], imagePrefixPlaceholder)
		if idx == -1 {
			break
		}
		start := offset + idx
		nameStart := start + len(imagePrefixPlaceholder)

		if nameStart >= len(s) || !isAlphaNumeric(s[nameStart]) {
			offset = nameStart
			continue
		}

		nameEnd := nameStart + 1
		for nameEnd < len(s) && isImageNameChar(s[nameEnd]) {
			nameEnd++
		}

		if !isAlphaNumeric(s[nameEnd-1]) {
			offset = nameEnd
			continue
		}

		imageName := s[nameStart:nameEnd]
		if strings.Contains(imageName, "//") {
			offset = nameEnd
			continue
		}

		fullEnd := nameEnd
		if nameEnd < len(s) && s[nameEnd] == ':' {
			if strings.HasPrefix(s[nameEnd:], imageTagPlaceholder) {
				tagEnd := nameEnd + len(imageTagPlaceholder)
				if tagEnd < len(s) && (isAlphaNumeric(s[tagEnd]) || s[tagEnd] == '-' || s[tagEnd] == '_' || s[tagEnd] == '.') {
					offset = nameEnd
					continue
				}
				fullEnd = tagEnd
			} else {
				// Non-matching tag (e.g. :latest or :v1)
				offset = nameEnd
				continue
			}
		}

		matches = append(matches, imageReferenceMatch{
			start:     start,
			end:       fullEnd,
			imageName: imageName,
		})

		offset = fullEnd
	}
	return matches
}

func collectStringImageReferences(node *yaml.Node, seen map[string]bool, names *[]string) {
	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			collectStringImageReferences(child, seen, names)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			valueNode := node.Content[i+1]
			if valueNode.Kind == yaml.ScalarNode {
				for _, match := range findImageReferenceMatches(valueNode.Value) {
					if !seen[match.imageName] {
						seen[match.imageName] = true
						*names = append(*names, match.imageName)
					}
				}
			}
			collectStringImageReferences(valueNode, seen, names)
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if child.Kind == yaml.ScalarNode {
				for _, match := range findImageReferenceMatches(child.Value) {
					if !seen[match.imageName] {
						seen[match.imageName] = true
						*names = append(*names, match.imageName)
					}
				}
			}
			collectStringImageReferences(child, seen, names)
		}
	}
}

// FindPlaceholderImages parses YAML content and returns a deduplicated, sorted slice
// of placeholder image names referenced in the content.
func FindPlaceholderImages(content string) ([]string, error) {
	decoder := yaml.NewDecoder(strings.NewReader(content))
	var placeholders []*yaml.Node
	seen := make(map[string]bool)
	var names []string

	for {
		var node yaml.Node
		err := decoder.Decode(&node)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to decode YAML: %w", err)
		}
		placeholders = collectPlaceholders(&node, placeholders, nil)
		collectStringImageReferences(&node, seen, &names)
	}

	for _, p := range placeholders {
		base, ok := isPlaceholderImage(p.Value)
		if !ok {
			continue
		}
		if !seen[base] {
			seen[base] = true
			names = append(names, base)
		}
	}

	if len(names) == 0 {
		return nil, nil
	}

	sort.Strings(names)
	return names, nil
}

// FindReferencedImagesInFile reads a YAML manifest file and returns referenced placeholder image names.
func FindReferencedImagesInFile(path string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	return FindPlaceholderImages(string(content))
}

// ResolveImageReference computes the substituted image reference for a placeholder image.
// If imageTag is not "latest", it returns <imageRepository>/<imageName>:<imageTag> without digest.
// If imageTag is "latest" (or empty), and a digest exists in digests, it emits
// <imageRepository>/<imageName>:<imageTag>@sha256:<digest> (or <imageRepository>/<imageName>:<digest>
// if digest is a fallback tag like latest-<hex>).
func ResolveImageReference(imageRepository, imageName, imageTag string, digests map[string]string) string {
	tag := imageTag
	if tag == "" {
		tag = "latest"
	}
	prefix := imageName
	if imageRepository != "" {
		prefix = fmt.Sprintf("%s/%s", imageRepository, imageName)
	}

	if tag != "latest" {
		return fmt.Sprintf("%s:%s", prefix, tag)
	}

	var digest string
	if digests != nil {
		digest = digests[imageName]
	}
	if digest == "" {
		return fmt.Sprintf("%s:%s", prefix, tag)
	}

	if strings.HasPrefix(digest, "latest-") {
		return fmt.Sprintf("%s:%s", prefix, digest)
	}

	if !strings.HasPrefix(digest, "sha256:") {
		digest = "sha256:" + digest
	}
	return fmt.Sprintf("%s:%s@%s", prefix, tag, digest)
}

func replacePlaceholderImages(content string, imageRepository string, imageTag string, imageDigests ...map[string]string) (string, error) {
	decoder := yaml.NewDecoder(strings.NewReader(content))
	var placeholders []*yaml.Node
	for {
		var node yaml.Node
		err := decoder.Decode(&node)
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("failed to decode YAML: %w", err)
		}
		placeholders = collectPlaceholders(&node, placeholders, nil)
	}

	var digests map[string]string
	if len(imageDigests) > 0 {
		digests = imageDigests[0]
	}

	lineOffsets := getLineOffsets(content)

	type replacement struct {
		offset int
		length int
		newVal string
	}
	var replacements []replacement

	for _, p := range placeholders {
		if p.Line == 0 || p.Line > len(lineOffsets) {
			return "", fmt.Errorf("invalid line number %d for placeholder %q", p.Line, p.Value)
		}
		start := lineOffsets[p.Line-1] + p.Column - 1
		if start >= len(content) {
			return "", fmt.Errorf("invalid column %d on line %d for placeholder %q", p.Column, p.Line, p.Value)
		}

		end := findEnd(content, start, p.Style)

		base, ok := isPlaceholderImage(p.Value)
		if !ok {
			return "", fmt.Errorf("invalid placeholder image %q", p.Value)
		}

		newVal := ResolveImageReference(imageRepository, base, imageTag, digests)
		replacements = append(replacements, replacement{
			offset: start,
			length: end - start,
			newVal: newVal,
		})
	}

	for _, match := range findImageReferenceMatches(content) {
		newVal := ResolveImageReference(imageRepository, match.imageName, imageTag, digests)
		replacements = append(replacements, replacement{
			offset: match.start,
			length: match.end - match.start,
			newVal: newVal,
		})
	}

	if len(replacements) == 0 {
		return content, nil
	}

	// Sort replacements in reverse order to apply them without affecting offsets
	sort.Slice(replacements, func(i, j int) bool {
		return replacements[i].offset > replacements[j].offset
	})

	for _, r := range replacements {
		content = content[:r.offset] + r.newVal + content[r.offset+r.length:]
	}

	return content, nil
}

func collectPlaceholders(node *yaml.Node, placeholders []*yaml.Node, path []string) []*yaml.Node {
	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			placeholders = collectPlaceholders(child, placeholders, path)
		}
	case yaml.MappingNode:
		for i := 0; i < len(node.Content); i += 2 {
			keyNode := node.Content[i]
			valueNode := node.Content[i+1]
			newPath := append(path, keyNode.Value)
			if keyNode.Value == "image" && valueNode.Kind == yaml.ScalarNode {
				if isImageField(newPath) {
					if _, ok := isPlaceholderImage(valueNode.Value); ok {
						placeholders = append(placeholders, valueNode)
					}
				}
			}
			placeholders = collectPlaceholders(valueNode, placeholders, newPath)
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			placeholders = collectPlaceholders(child, placeholders, append(path, "*"))
		}
	}
	return placeholders
}

func isPlaceholderImage(image string) (string, bool) {
	if image == "" {
		return "", false
	}

	ref, err := imageref.Parse(image)
	if err != nil {
		return "", false
	}

	if ref.Digest != "" {
		return "", false
	}

	if ref.Tag != "" && ref.Tag != "latest" {
		return "", false
	}

	if ref.Registry != "" {
		return "", false
	}

	return ref.Repository, true
}

func isImageField(path []string) bool {
	p := strings.Join(path, ".")
	switch p {
	case "image",
		"spec.containers.*.image",
		"spec.initContainers.*.image",
		"spec.template.spec.containers.*.image",
		"spec.template.spec.initContainers.*.image",
		"spec.jobTemplate.spec.template.spec.containers.*.image",
		"spec.jobTemplate.spec.template.spec.initContainers.*.image",
		"spec.podTemplate.spec.containers.*.image",
		"spec.podTemplate.spec.initContainers.*.image":
		return true
	}
	return false
}

func getLineOffsets(content string) []int {
	offsets := []int{0}
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			offsets = append(offsets, i+1)
		}
	}
	return offsets
}

func findEnd(content string, start int, style yaml.Style) int {
	if style&yaml.DoubleQuotedStyle != 0 {
		for i := start + 1; i < len(content); i++ {
			if content[i] == '"' {
				backslashes := 0
				for j := i - 1; j >= start; j-- {
					if content[j] == '\\' {
						backslashes++
					} else {
						break
					}
				}
				if backslashes%2 == 0 {
					return i + 1
				}
			}
		}
	} else if style&yaml.SingleQuotedStyle != 0 {
		for i := start + 1; i < len(content); i++ {
			if content[i] == '\'' {
				if i+1 < len(content) && content[i+1] == '\'' {
					i++
					continue
				}
				return i + 1
			}
		}
	} else {
		for i := start; i < len(content); i++ {
			c := content[i]
			if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '#' || c == ',' || c == ']' || c == '}' {
				return i
			}
		}
	}
	return len(content)
}

// DeployTaskOptions holds configuration options for deploying manifests.
type DeployTaskOptions struct {
	Namespace         string
	OriginalNamespace string
	ScopeResolver     ScopeResolver
	CheckNamespace    func(ctx context.Context, namespace string) error
	ImageRepository   string
	ImageTag          string
	ImageDigests      map[string]string
}

// KubectlApplyTask represents a task to apply a single k8s manifest.
type KubectlApplyTask struct {
	ManifestPath      string
	Namespace         string
	OriginalNamespace string
	ScopeResolver     ScopeResolver
	CheckNamespace    func(ctx context.Context, namespace string) error
	ImageRepository   string
	ImageTag          string
	ImageDigests      map[string]string
}

func (t *KubectlApplyTask) Run(ctx context.Context, scope *tasks.APScope) error {
	imageRepository := t.ImageRepository
	if imageRepository == "" {
		cfg, err := config.Load(scope.RepoRoot)
		if err != nil {
			return err
		}
		imageRepository = cfg.ImageRepo()
		if imageRepository == "" {
			return fmt.Errorf("IMAGE_PREFIX is not set; it is required for deploy")
		}
	}
	tag := t.ImageTag
	if tag == "" {
		tag = config.ImageTag()
	}

	relPath, err := filepath.Rel(scope.RepoRoot, t.ManifestPath)
	if err != nil {
		return err
	}
	klog.Infof("Applying manifest %s", relPath)

	content, err := os.ReadFile(t.ManifestPath)
	if err != nil {
		return err
	}

	replaced, err := replacePlaceholderImages(string(content), imageRepository, tag, t.ImageDigests)
	if err != nil {
		return fmt.Errorf("failed to replace placeholders in %s: %w", relPath, err)
	}

	if t.Namespace != "" {
		checkFn := t.CheckNamespace
		if checkFn == nil {
			checkFn = DefaultCheckNamespaceCached
		}
		if err := checkFn(ctx, t.Namespace); err != nil {
			return err
		}

		resolver := t.ScopeResolver
		if resolver == nil {
			var err error
			resolver, err = GetDiscoveryScopeResolver()
			if err != nil {
				return fmt.Errorf("failed to get discovery scope resolver: %w", err)
			}
		}

		retargeted, clusterScoped, err := RetargetManifestNamespace(replaced, t.Namespace, resolver, t.OriginalNamespace)
		if err != nil {
			return fmt.Errorf("failed to retarget namespace in %s: %w", relPath, err)
		}
		if len(clusterScoped) > 0 {
			klog.Warningf("Cluster-scoped object(s) left unchanged in %s: %s (names may collide between deployments into different namespaces)", relPath, strings.Join(clusterScoped, ", "))
		}
		replaced = retargeted
	}

	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = bytes.NewBufferString(replaced)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl apply failed for %s: %w", relPath, err)
	}

	// If this was a CRD, wait for it to be established
	crdNames, err := getCRDNames(content)
	if err != nil {
		klog.Warningf("Failed to check if %s is a CRD: %v", relPath, err)
	} else {
		for _, name := range crdNames {
			klog.Infof("Waiting for CRD %s to be established...", name)
			waitCmd := exec.CommandContext(ctx, "kubectl", "wait", "--for=condition=Established", "customresourcedefinition/"+name, "--timeout=60s")
			waitCmd.Stdout = os.Stdout
			waitCmd.Stderr = os.Stderr
			if err := waitCmd.Run(); err != nil {
				return fmt.Errorf("failed to wait for CRD %s: %w", name, err)
			}
		}
	}

	return nil
}

func (t *KubectlApplyTask) GetName() string {
	if t.Namespace != "" {
		return fmt.Sprintf("kubectl-apply-%s (namespace: %s)", filepath.Base(t.ManifestPath), t.Namespace)
	}
	return fmt.Sprintf("kubectl-apply-%s", filepath.Base(t.ManifestPath))
}

func (t *KubectlApplyTask) GetChildren() []tasks.Task {
	imageRepository := t.ImageRepository
	imageTag := t.ImageTag
	if imageRepository == "" {
		if dir := filepath.Dir(t.ManifestPath); dir != "" {
			if cfg, err := config.Load(dir); err == nil && cfg != nil {
				imageRepository = cfg.ImageRepo()
			}
		}
	}
	if imageTag == "" {
		imageTag = config.ImageTag()
	}
	if imageRepository == "" {
		return nil
	}

	referenced, err := FindReferencedImagesInFile(t.ManifestPath)
	if err != nil || len(referenced) == 0 {
		return nil
	}

	var children []tasks.Task
	for _, img := range referenced {
		ref := ResolveImageReference(imageRepository, img, imageTag, t.ImageDigests)
		children = append(children, &tasks.DummyTask{
			Name: ref,
		})
	}
	return children
}

func getCRDNames(content []byte) ([]string, error) {
	var names []string
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	for {
		var doc yaml.Node
		if err := decoder.Decode(&doc); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		// Basic check for CRD
		// We look for kind: CustomResourceDefinition and metadata.name
		if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
			root := doc.Content[0]
			if root.Kind == yaml.MappingNode {
				isCRD := false
				name := ""
				for i := 0; i+1 < len(root.Content); i += 2 {
					key := root.Content[i].Value
					val := root.Content[i+1]
					if key == "kind" && val.Kind == yaml.ScalarNode && val.Value == "CustomResourceDefinition" {
						isCRD = true
					}
					if key == "metadata" && val.Kind == yaml.MappingNode {
						for j := 0; j+1 < len(val.Content); j += 2 {
							mKey := val.Content[j].Value
							mVal := val.Content[j+1]
							if mKey == "name" && mVal.Kind == yaml.ScalarNode {
								name = mVal.Value
							}
						}
					}
				}
				if isCRD && name != "" {
					names = append(names, name)
				}
			}
		}
	}
	return names, nil
}

// DeployTasksForManifests returns a task group for deploying the specified k8s manifests.
func DeployTasksForManifests(manifests []string, opts ...DeployTaskOptions) tasks.Task {
	var opt DeployTaskOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	var deployTasks []tasks.Task
	for _, manifest := range manifests {
		deployTasks = append(deployTasks, &KubectlApplyTask{
			ManifestPath:      manifest,
			Namespace:         opt.Namespace,
			OriginalNamespace: opt.OriginalNamespace,
			ScopeResolver:     opt.ScopeResolver,
			CheckNamespace:    opt.CheckNamespace,
			ImageRepository:   opt.ImageRepository,
			ImageTag:          opt.ImageTag,
			ImageDigests:      opt.ImageDigests,
		})
	}

	return &tasks.Group{
		Name:  "deploy-k8s",
		Tasks: deployTasks,
	}
}

// DeployTasks returns a task group for deploying all k8s manifests found in k8s directories.
func DeployTasks(r *repo.Repo, root string) (tasks.Task, error) {
	manifests, err := findManifests(r, root)
	if err != nil {
		return nil, err
	}
	return DeployTasksForManifests(manifests), nil
}

// Deploy deploys k8s manifests found in k8s directories.
func Deploy(ctx context.Context, r *repo.Repo, root string) error {
	t, err := DeployTasks(r, root)
	if err != nil {
		return err
	}
	return t.Run(ctx, &tasks.APScope{RepoRoot: root, Dir: root, Repo: r})
}

func findManifests(r *repo.Repo, root string) ([]string, error) {
	return r.Under(root).FindManifests(), nil
}
