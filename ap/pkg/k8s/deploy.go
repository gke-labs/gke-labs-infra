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
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gke-labs/gke-labs-infra/codestyle/pkg/walker"
	"k8s.io/klog/v2"
)

var imageRegex = regexp.MustCompile(`image:(\s+)(\S+)`)

func replacePlaceholderImages(content string) string {
	return imageRegex.ReplaceAllStringFunc(content, func(match string) string {
		submatches := imageRegex.FindStringSubmatch(match)
		if len(submatches) < 3 {
			return match
		}
		spaces := submatches[1]
		image := submatches[2]

		// Remove optional quotes for checking
		unquoted := strings.Trim(image, "\"'")

		if strings.Contains(unquoted, "/") || strings.Contains(unquoted, ":") {
			return match
		}

		// It's a placeholder.
		return fmt.Sprintf("image:%s${IMAGE_PREFIX}/%s:${IMAGE_TAG}", spaces, unquoted)
	})
}

// Deploy deploys k8s manifests found in k8s directories.
func Deploy(ctx context.Context, root string) error {
	manifests, err := findManifests(root)
	if err != nil {
		return err
	}

	imagePrefix := os.Getenv("IMAGE_PREFIX")
	if imagePrefix == "" {
		// Ensure it is set for expansion
		os.Setenv("IMAGE_PREFIX", "local")
	}
	tag := os.Getenv("IMAGE_TAG")
	if tag == "" {
		// Ensure it is set for expansion
		os.Setenv("IMAGE_TAG", "latest")
	}

	for _, manifest := range manifests {
		relPath, _ := filepath.Rel(root, manifest)

		klog.Infof("Applying manifest %s", relPath)

		content, err := os.ReadFile(manifest)
		if err != nil {
			return err
		}

		replaced := replacePlaceholderImages(string(content))
		expanded := os.ExpandEnv(replaced)

		cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
		cmd.Stdin = bytes.NewBufferString(expanded)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("kubectl apply failed for %s: %w", relPath, err)
		}
	}
	return nil
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
