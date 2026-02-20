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

package images

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/codestyle/pkg/walker"
	"k8s.io/klog/v2"
)

// Build builds docker images found in images/<name>/Dockerfile.
func Build(ctx context.Context, root string) error {
	imagePrefix := os.Getenv("IMAGE_PREFIX")
	if imagePrefix == "" {
		return fmt.Errorf("IMAGE_PREFIX is not set; it is required for building images")
	}
	tag := os.Getenv("IMAGE_TAG")
	if tag == "" {
		tag = "latest"
	}

	dockerfiles, err := findDockerfiles(root)
	if err != nil {
		return err
	}

	for _, dockerfile := range dockerfiles {
		relPath, err := filepath.Rel(root, dockerfile)
		if err != nil {
			continue
		}

		name := getImageName(relPath)
		if name == "" {
			continue
		}

		fullImageName := fmt.Sprintf("%s/%s:%s", imagePrefix, name, tag)

		klog.Infof("Building image %s from %s", fullImageName, root)
		cmd := exec.CommandContext(ctx, "docker", "buildx", "build", "-t", fullImageName, "-f", relPath, ".")
		cmd.Dir = root
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("docker build failed for %s: %w", name, err)
		}
	}
	return nil
}

// Push pushes docker images found in images/<name>/Dockerfile.
func Push(ctx context.Context, root string) error {
	imagePrefix := os.Getenv("IMAGE_PREFIX")
	if imagePrefix == "" {
		return fmt.Errorf("IMAGE_PREFIX is not set; it is required for pushing images")
	}
	tag := os.Getenv("IMAGE_TAG")
	if tag == "" {
		tag = "latest"
	}

	dockerfiles, err := findDockerfiles(root)
	if err != nil {
		return err
	}

	for _, dockerfile := range dockerfiles {
		relPath, err := filepath.Rel(root, dockerfile)
		if err != nil {
			continue
		}

		name := getImageName(relPath)
		if name == "" {
			continue
		}

		fullImageName := fmt.Sprintf("%s/%s:%s", imagePrefix, name, tag)

		klog.Infof("Pushing image %s", fullImageName)
		cmd := exec.CommandContext(ctx, "docker", "push", fullImageName)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("docker push failed for %s: %w", name, err)
		}
	}
	return nil
}

// HasImages returns true if there are any images to build under root.
func HasImages(root string) (bool, error) {
	dockerfiles, err := findDockerfiles(root)
	if err != nil {
		return false, err
	}

	for _, dockerfile := range dockerfiles {
		relPath, err := filepath.Rel(root, dockerfile)
		if err != nil {
			continue
		}
		if getImageName(relPath) != "" {
			return true, nil
		}
	}

	return false, nil
}

func findDockerfiles(root string) ([]string, error) {
	ignoreList := walker.NewIgnoreList([]string{".git", "vendor", "node_modules"})

	var dockerfiles []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}

		if ignoreList.ShouldIgnore(relPath, info.IsDir()) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.IsDir() {
			// If this directory contains a .ap directory, it's a different root, so skip it.
			if _, err := os.Stat(filepath.Join(path, ".ap")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}

		if info.Name() == "Dockerfile" {
			dockerfiles = append(dockerfiles, path)
		}
		return nil
	})
	return dockerfiles, err
}

func getImageName(relPath string) string {
	parts := strings.Split(relPath, string(os.PathSeparator))

	// Look for images/<name>/Dockerfile structure
	for i, part := range parts {
		if part == "images" && i+2 < len(parts) && parts[len(parts)-1] == "Dockerfile" {
			return parts[i+1]
		}
	}
	return ""
}
