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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/k8s"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	toolDocker "github.com/gke-labs/gke-labs-infra/ap/pkg/tool/docker"
	"k8s.io/klog/v2"
)

var execCommandContext = exec.CommandContext

var ensureBuildx = defaultEnsureBuildx
var ensureDocker = defaultEnsureDocker

func defaultEnsureDocker(ctx context.Context, repoRoot string) (string, error) {
	cfg, err := toolDocker.LoadConfig(repoRoot)
	if err != nil {
		return "", err
	}
	if cfg == nil {
		cfg = toolDocker.DefaultConfig()
	}
	return toolDocker.EnsureDocker(ctx, cfg.Docker)
}

func defaultEnsureBuildx(ctx context.Context, repoRoot string) (string, error) {
	cfg, err := toolDocker.LoadConfig(repoRoot)
	if err != nil {
		return "", err
	}
	if cfg == nil {
		cfg = toolDocker.DefaultConfig()
	}
	return toolDocker.EnsureDockerBuildx(ctx, cfg.Buildx)
}

// DefaultK8sBuildkitHost is the default buildkit host used for the k8s shorthand.
const DefaultK8sBuildkitHost = "k8s://autodeploy-system/buildkit"

// ResolveBuildkitHost applies the env fallback, expands "k8s", and validates the k8s://namespace/service shape.
func ResolveBuildkitHost(value string) (string, error) {
	if value == "" {
		value = os.Getenv("BUILDKIT_HOST")
	}
	if value == "k8s" {
		value = DefaultK8sBuildkitHost
	}
	if host, ok := strings.CutPrefix(value, "k8s://"); ok {
		parts := strings.Split(host, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", fmt.Errorf("invalid buildkit host: %s, expected k8s://namespace/service", value)
		}
	}
	return value, nil
}

// FullImageName returns the fully-qualified image tag given prefix, image name, and tag.
func FullImageName(imagePrefix, imageName, tag string) string {
	return ImageRef{Prefix: imagePrefix, Name: imageName, Tag: tag}.String()
}

// DockerBuildTask represents a task to build a single docker image.
type DockerBuildTask struct {
	ImageName    string
	Dockerfile   string
	Root         string
	Push         bool
	BuildkitHost string
	DockerBin    string
	BuildxBin    string
	Naming       ImageNaming
}

func (t *DockerBuildTask) Run(ctx context.Context, scope *tasks.TaskContext) error {
	imagesCfg, err := config.LoadImagesConfig(scope.RepoRoot())
	if err != nil {
		return err
	}
	platforms := imagesCfg.GetPlatforms()

	naming := t.Naming
	if naming.Tag == "" && naming.Prefix == "" {
		cfg, err := config.Load(scope.RepoRoot())
		if err != nil {
			return err
		}
		naming = ResolveImageNaming(cfg)
	}

	actualImagePrefix := naming.Prefix
	if t.Push {
		actualImagePrefix = naming.PushPrefix(t.BuildkitHost)
	}

	fullImageName := ImageRef{Prefix: actualImagePrefix, Name: t.ImageName, Tag: naming.Tag}.String()

	if t.BuildkitHost != "" {
		return t.runBuildctl(ctx, scope, t.Root, fullImageName, t.Dockerfile, naming, platforms)
	}

	buildxBin := t.BuildxBin
	if buildxBin == "" && t.DockerBin != "" {
		buildxBin = t.DockerBin
	}
	if buildxBin == "" {
		var err error
		buildxBin, err = ensureBuildx(ctx, scope.RepoRoot())
		if err != nil {
			return fmt.Errorf("failed to ensure docker-buildx: %w", err)
		}
	}

	dockerBin := t.DockerBin
	if dockerBin == "" {
		if d, err := ensureDocker(ctx, scope.RepoRoot()); err == nil && d != "" {
			dockerBin = d
		} else {
			dockerBin = "docker"
		}
	}

	// If we are pushing and have requested multiple platforms, but the current Docker driver
	// doesn't support multi-platform builds (e.g., standard default docker driver), we first
	// attempt to use or create a multi-platform capable buildx builder (ap-builder).
	// If it still cannot support multi-platform builds, we treat this as an error and provide hints.
	// If we are building locally without pushing, we fall back to the host's native platform
	// so local developer builds don't fail due to repository-wide multi-platform settings.
	if t.Push && len(platforms) > 1 {
		if !t.supportsMultiPlatform(ctx, buildxBin) {
			if err := t.ensureMultiPlatformBuilder(ctx, buildxBin); err != nil {
				return fmt.Errorf("failed to ensure multi-platform builder: %w", err)
			}
		}
		if !t.supportsMultiPlatform(ctx, buildxBin) {
			return fmt.Errorf("multi-platform build (platforms: %v) is not supported for the default docker driver. "+
				"To build for multiple platforms, please switch to a different driver (e.g., docker-container, kubernetes) "+
				"by running 'docker buildx create --use', or configure a single platform in '.ap/images.yaml'", platforms)
		}
	} else if !t.Push && len(platforms) > 1 {
		klog.Infof("Building locally without pushing; falling back to native platform")
		platforms = nil
	}

	return t.runDocker(ctx, scope, buildxBin, dockerBin, t.Root, fullImageName, actualImagePrefix, t.Dockerfile, naming, platforms)
}

func (t *DockerBuildTask) ensureMultiPlatformBuilder(ctx context.Context, buildxBin string) error {
	if t.supportsMultiPlatform(ctx, buildxBin) {
		return nil
	}
	// Try to use existing ap-builder
	useCmd := execCommandContext(ctx, buildxBin, "use", "ap-builder")
	useCmd.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(buildxBin))
	if err := useCmd.Run(); err == nil && t.supportsMultiPlatform(ctx, buildxBin) {
		return nil
	}
	// Create new ap-builder with docker-container driver and host networking
	createCmd := execCommandContext(ctx, buildxBin, "create", "--name", "ap-builder", "--driver", "docker-container", "--driver-opt", "network=host", "--use")
	createCmd.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(buildxBin))
	if err := createCmd.Run(); err != nil {
		return fmt.Errorf("failed to create multi-platform builder: %w", err)
	}
	return nil
}

func (t *DockerBuildTask) supportsMultiPlatform(ctx context.Context, buildxBin string) bool {
	cmd := execCommandContext(ctx, buildxBin, "inspect")
	cmd.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(buildxBin))
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	// Parse the output to check the driver
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(line), "driver:") {
			val := strings.TrimSpace(line[7:])
			if strings.ToLower(val) == "docker" {
				return false
			}
		}
	}
	return true
}

func (t *DockerBuildTask) runBuildctl(ctx context.Context, scope *tasks.TaskContext, root, fullImageName, relDockerfilePath string, naming ImageNaming, platforms []string) error {
	klog.Infof("Building image %s from %s using buildctl", fullImageName, root)

	output := fmt.Sprintf("type=image,name=%s,push=%t", fullImageName, t.Push)
	if t.Push {
		output += ",registry.insecure=true"
	}

	metaFile := filepath.Join(os.TempDir(), fmt.Sprintf("buildctl-meta-%d.json", time.Now().UnixNano()))
	defer func() {
		if err := os.Remove(metaFile); err != nil && !os.IsNotExist(err) {
			klog.V(4).Infof("failed to remove metadata file %s: %v", metaFile, err)
		}
	}()

	buildctlArgs := []string{
		"build",
		"--frontend", "dockerfile.v0",
		"--local", "context=.",
		"--local", "dockerfile=.",
		"--opt", "filename=" + relDockerfilePath,
		"--opt", "build-arg:IMAGE_PREFIX=" + naming.Prefix,
		"--opt", "build-arg:IMAGE_TAG=" + naming.Tag,
		"--metadata-file", metaFile,
		"--output", output,
	}

	if len(platforms) > 0 {
		buildctlArgs = append(buildctlArgs, "--opt", "platform="+strings.Join(platforms, ","))
	}

	if host, ok := strings.CutPrefix(t.BuildkitHost, "k8s://"); ok {
		// handle port forward
		parts := strings.Split(host, "/")
		// Defensive check; host is already validated by ResolveBuildkitHost.
		if len(parts) != 2 {
			return fmt.Errorf("invalid buildkit host: %s, expected k8s://namespace/service", t.BuildkitHost)
		}
		namespace, service := parts[0], parts[1]

		pfTask := &k8s.PortForwardTask{
			Child: &tasks.FuncTask{
				Name: "run-buildctl",
				RunFn: func(ctx context.Context, scope *tasks.TaskContext) error {
					cmd := execCommandContext(ctx, "buildctl", buildctlArgs...)
					cmd.Dir = root
					cmd.Stdout = scope.Stdout()
					cmd.Stderr = scope.Stderr()
					cmd.Env = append(os.Environ(), "BUILDKIT_HOST=tcp://localhost:2375")
					return cmd.Run()
				},
			},
			Service:    service,
			Namespace:  namespace,
			LocalPort:  2375,
			RemotePort: 2375,
		}
		childScope := tasks.NewTaskContext(root, root, scope.SourceTree()).WithOutput(scope.Stdout(), scope.Stderr())
		if err := pfTask.Run(ctx, childScope); err != nil {
			return err
		}
	} else {
		cmd := execCommandContext(ctx, "buildctl", buildctlArgs...)
		cmd.Dir = root
		cmd.Stdout = scope.Stdout()
		cmd.Stderr = scope.Stderr()
		cmd.Env = append(os.Environ(), "BUILDKIT_HOST="+t.BuildkitHost)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("buildctl build failed for %s: %w", t.ImageName, err)
		}
	}

	digest := extractDigestFromMetadataFile(metaFile)
	if naming.Tag == "latest" && digest != "" {
		ref := naming.Ref(t.ImageName)
		ref.Digest = digest
	}

	return nil
}

func (t *DockerBuildTask) runDocker(ctx context.Context, scope *tasks.TaskContext, buildxBin, dockerBin, root, fullImageName, actualImagePrefix, relDockerfilePath string, naming ImageNaming, platforms []string) error {
	klog.Infof("Building image %s from %s using docker buildx", fullImageName, root)

	metaFile := filepath.Join(os.TempDir(), fmt.Sprintf("buildx-meta-%d.json", time.Now().UnixNano()))
	defer func() {
		if err := os.Remove(metaFile); err != nil && !os.IsNotExist(err) {
			klog.V(4).Infof("failed to remove metadata file %s: %v", metaFile, err)
		}
	}()

	args := []string{
		"build",
		"-t", fullImageName,
		"-f", relDockerfilePath,
		"--build-arg", "IMAGE_PREFIX=" + naming.Prefix,
		"--build-arg", "IMAGE_TAG=" + naming.Tag,
		"--metadata-file", metaFile,
	}

	if t.Push {
		args = append(args, "--push")
		if len(platforms) > 0 {
			args = append(args, "--platform", strings.Join(platforms, ","))
		}
	} else {
		// When building locally without pushing, use --load to load the built image
		// into the local Docker daemon image store. Multi-platform is not supported with --load
		// because the daemon image store cannot hold images for different platforms.
		args = append(args, "--load")
		if len(platforms) == 1 {
			args = append(args, "--platform", platforms[0])
		}
	}
	args = append(args, ".")

	cmd := execCommandContext(ctx, buildxBin, args...)
	cmd.Dir = root
	cmd.Stdout = scope.Stdout()
	cmd.Stderr = scope.Stderr()
	cmd.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(buildxBin))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build failed for %s: %w", t.ImageName, err)
	}

	digest := extractDigestFromMetadataFile(metaFile)
	if digest == "" {
		digest = inspectImageDigest(ctx, dockerBin, fullImageName)
	}

	if naming.Tag == "latest" && digest != "" {
		if naming.KindMode {
			hex := strings.TrimPrefix(digest, "sha256:")
			if len(hex) > 12 {
				hex = hex[:12]
			}
			uniqueTag := fmt.Sprintf("latest-%s", hex)
			uniqueImageName := ImageRef{Prefix: actualImagePrefix, Name: t.ImageName, Tag: uniqueTag}.String()
			tagCmd := execCommandContext(ctx, dockerBin, "tag", fullImageName, uniqueImageName)
			tagCmd.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(dockerBin))
			if err := tagCmd.Run(); err != nil {
				return fmt.Errorf("failed to tag %s as %s: %w", fullImageName, uniqueImageName, err)
			}
			ref := naming.Ref(t.ImageName)
			ref.Tag = uniqueTag
			ref.Digest = ""
		} else {
			ref := naming.Ref(t.ImageName)
			ref.Digest = digest
		}
	}

	return nil
}

type buildMetadataFile struct {
	Digest string `json:"containerimage.digest"`
}

func extractDigestFromMetadataFile(metaPath string) string {
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return ""
	}
	var meta buildMetadataFile
	if err := json.Unmarshal(data, &meta); err != nil {
		return ""
	}
	return strings.TrimSpace(meta.Digest)
}

func inspectImageDigest(ctx context.Context, dockerBin, fullImageName string) string {
	cmd := execCommandContext(ctx, dockerBin, "image", "inspect", "--format", "{{if .Descriptor}}{{.Descriptor.digest}}{{end}}", fullImageName)
	cmd.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(dockerBin))
	out, err := cmd.Output()
	if err == nil {
		d := strings.TrimSpace(string(out))
		if d != "" && d != "<no value>" {
			return d
		}
	}

	cmd = execCommandContext(ctx, dockerBin, "image", "inspect", "--format", "{{index .RepoDigests 0}}", fullImageName)
	cmd.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(dockerBin))
	out, err = cmd.Output()
	if err == nil {
		d := strings.TrimSpace(string(out))
		if idx := strings.LastIndex(d, "@"); idx != -1 {
			return d[idx+1:]
		}
	}

	cmd = execCommandContext(ctx, dockerBin, "image", "inspect", "--format", "{{.Id}}", fullImageName)
	cmd.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(dockerBin))
	out, err = cmd.Output()
	if err == nil {
		id := strings.TrimSpace(string(out))
		if id != "" {
			return id
		}
	}

	return ""
}

func (t *DockerBuildTask) GetName() string {
	return fmt.Sprintf("docker-build-%s", t.ImageName)
}

func (t *DockerBuildTask) GetChildren() []tasks.Task {
	return nil
}

// FindImageNames returns the sorted names of all Docker images defined in the given root.
func FindImageNames(r *sourcetree.SourceTree, root string) ([]string, error) {
	dockerfiles, err := findDockerfiles(r, root)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]string)
	var names []string
	for _, dockerfile := range dockerfiles {
		relPath, err := filepath.Rel(root, dockerfile)
		if err != nil {
			continue
		}

		name := getImageName(relPath)
		if name != "" {
			if existing, ok := seen[name]; ok {
				return nil, fmt.Errorf("duplicate image %q found in %s and %s", name, existing, dockerfile)
			}
			seen[name] = dockerfile
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// BuildTasksForImages returns a task group for building only the specified docker images.
func BuildTasksForImages(r *sourcetree.SourceTree, root string, push bool, buildkitHost string, imageNames []string, naming ImageNaming) (tasks.Task, error) {
	if _, err := FindImageNames(r, root); err != nil {
		return nil, err
	}

	dockerfiles, err := findDockerfiles(r, root)
	if err != nil {
		return nil, err
	}

	filter := make(map[string]bool, len(imageNames))
	for _, name := range imageNames {
		filter[name] = true
	}

	repoRoot := r.RepoRoot()
	if repoRoot == "" {
		repoRoot = root
	}

	var buildTasks []tasks.Task
	for _, dockerfile := range dockerfiles {
		relToRoot, err := filepath.Rel(root, dockerfile)
		if err != nil {
			continue
		}

		name := getImageName(relToRoot)
		if name == "" || !filter[name] {
			continue
		}

		relToRepo, err := filepath.Rel(repoRoot, dockerfile)
		if err != nil {
			continue
		}

		buildTasks = append(buildTasks, &DockerBuildTask{
			ImageName:    name,
			Dockerfile:   relToRepo,
			Root:         repoRoot,
			Push:         push,
			BuildkitHost: buildkitHost,
			Naming:       naming,
		})
	}

	var rootTask tasks.Task = &tasks.Group{
		Name:     "build-images",
		Tasks:    buildTasks,
		Parallel: true,
	}

	if len(buildTasks) == 0 {
		return rootTask, nil
	}

	if push && naming.Prefix == "images.local" && !strings.HasPrefix(buildkitHost, "k8s://") {
		rootTask = &k8s.PortForwardTask{
			Child:      rootTask,
			Service:    "in-cluster-image-registry",
			Namespace:  "in-cluster-image-registry-system",
			LocalPort:  5000,
			RemotePort: 80,
		}
	}

	return rootTask, nil
}

// BuildTasks returns a task group for building all docker images found in images/<name>/Dockerfile.
func BuildTasks(r *sourcetree.SourceTree, root string, push bool, buildkitHost string, naming ImageNaming) (tasks.Task, error) {
	allImages, err := FindImageNames(r, root)
	if err != nil {
		return nil, err
	}
	return BuildTasksForImages(r, root, push, buildkitHost, allImages, naming)
}

// Build builds docker images found in images/<name>/Dockerfile.
func Build(ctx context.Context, r *sourcetree.SourceTree, root string, push bool, buildkitHost string) error {
	cfg, err := config.Load(root)
	if err != nil {
		return err
	}
	naming := ResolveImageNaming(cfg)
	t, err := BuildTasks(r, root, push, buildkitHost, naming)
	if err != nil {
		return err
	}
	return t.Run(ctx, tasks.NewTaskContext(root, root, r))
}

// HasImages returns true if there are any images to build under root.
func HasImages(r *sourcetree.SourceTree, root string) (bool, error) {
	dockerfiles, err := findDockerfiles(r, root)
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

func findDockerfiles(r *sourcetree.SourceTree, root string) ([]string, error) {
	return r.Under(root).FindDockerfiles(), nil
}

func getImageName(relPath string) string {
	parts := strings.Split(relPath, string(os.PathSeparator))

	// Look for images/<name>/Dockerfile structure
	for i, part := range parts {
		if part == "images" && i+2 < len(parts) && parts[i+2] == "Dockerfile" {
			return parts[i+1]
		}
	}
	return ""
}
