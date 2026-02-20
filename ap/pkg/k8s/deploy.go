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

	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"github.com/gke-labs/gke-labs-infra/codestyle/pkg/walker"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"
)

func replacePlaceholderImages(content string, imageRepository string, imageTag string) (string, error) {
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

	if len(placeholders) == 0 {
		return content, nil
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

		newVal := fmt.Sprintf("%s/%s:%s", imageRepository, base, imageTag)
		replacements = append(replacements, replacement{
			offset: start,
			length: end - start,
			newVal: newVal,
		})
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

	// Handle digest if any (we probably want to skip these too)
	if strings.Contains(image, "@") {
		return "", false
	}

	base := image
	tag := ""
	if i := strings.LastIndex(image, ":"); i != -1 {
		lastPart := image[i+1:]
		if !strings.Contains(lastPart, "/") {
			base = image[:i]
			tag = lastPart
		}
	}

	if tag != "" && tag != "latest" {
		return "", false
	}

	// Check for host
	firstSlash := strings.Index(image, "/")
	if firstSlash != -1 {
		host := image[:firstSlash]
		if strings.Contains(host, ".") || strings.Contains(host, ":") || host == "localhost" {
			return "", false
		}
	}

	return base, true
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

// KubectlApplyTask represents a task to apply a single k8s manifest.
type KubectlApplyTask struct {
	ManifestPath string
}

func (t *KubectlApplyTask) Run(ctx context.Context, root string) error {
	imageRepository := os.Getenv("IMAGE_PREFIX")
	if imageRepository == "" {
		return fmt.Errorf("IMAGE_PREFIX is not set; it is required for deploy")
	}
	tag := os.Getenv("IMAGE_TAG")
	if tag == "" {
		tag = "latest"
	}

	relPath, _ := filepath.Rel(root, t.ManifestPath)
	klog.Infof("Applying manifest %s", relPath)

	content, err := os.ReadFile(t.ManifestPath)
	if err != nil {
		return err
	}

	replaced, err := replacePlaceholderImages(string(content), imageRepository, tag)
	if err != nil {
		return fmt.Errorf("failed to replace placeholders in %s: %w", relPath, err)
	}

	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = bytes.NewBufferString(replaced)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl apply failed for %s: %w", relPath, err)
	}
	return nil
}

func (t *KubectlApplyTask) GetName() string {
	return fmt.Sprintf("kubectl-apply-%s", filepath.Base(t.ManifestPath))
}

func (t *KubectlApplyTask) GetChildren() []tasks.Task {
	return nil
}

// DeployTasks returns a task group for deploying all k8s manifests found in k8s directories.
func DeployTasks(root string) (tasks.Task, error) {
	manifests, err := findManifests(root)
	if err != nil {
		return nil, err
	}

	var deployTasks []tasks.Task
	for _, manifest := range manifests {
		deployTasks = append(deployTasks, &KubectlApplyTask{
			ManifestPath: manifest,
		})
	}

	return &tasks.Group{
		Name:  "deploy-k8s",
		Tasks: deployTasks,
	}, nil
}

// Deploy deploys k8s manifests found in k8s directories.
func Deploy(ctx context.Context, root string) error {
	t, err := DeployTasks(root)
	if err != nil {
		return err
	}
	return t.Run(ctx, root)
}

func findManifests(root string) ([]string, error) {
	ignoreList := walker.NewIgnoreList([]string{".git", "vendor", "node_modules"})
	return walker.Walk(root, ignoreList, func(path string, info os.FileInfo) bool {
		if info.IsDir() {
			return false
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return false
		}

		// Check if it is under a k8s directory
		parts := strings.Split(relPath, string(os.PathSeparator))
		inK8s := false
		for _, part := range parts {
			if part == "k8s" {
				inK8s = true
				break
			}
		}
		if !inK8s {
			return false
		}

		ext := filepath.Ext(path)
		return ext == ".yaml" || ext == ".yml"
	})
}
