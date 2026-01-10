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
		klog.Warningf("IMAGE_PREFIX is not set, defaulting to 'local'")
		imagePrefix = "local"
	}

	dockerfiles, err := walker.Walk(root, []string{".git", "vendor", "node_modules"}, func(path string, info os.FileInfo) bool {
		return info.Name() == "Dockerfile"
	})
	if err != nil {
		return err
	}

	for _, dockerfile := range dockerfiles {
		relPath, err := filepath.Rel(root, dockerfile)
		if err != nil {
			continue
		}

		parts := strings.Split(relPath, string(os.PathSeparator))
		name := ""
		
		// Look for images/<name>/Dockerfile structure
		for i, part := range parts {
			if part == "images" && i+2 < len(parts) && parts[len(parts)-1] == "Dockerfile" {
				name = parts[i+1]
				break
			}
		}

		if name == "" {
			klog.V(2).Infof("Skipping Dockerfile not in images/<name>/Dockerfile structure: %s", relPath)
			continue
		}

		tag := fmt.Sprintf("%s/%s:latest", imagePrefix, name)
		dir := filepath.Dir(dockerfile)

		klog.Infof("Building image %s from %s", tag, dir)
		cmd := exec.CommandContext(ctx, "docker", "buildx", "build", "-t", tag, ".")
		cmd.Dir = dir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("docker build failed for %s: %w", name, err)
		}
	}
	return nil
}
