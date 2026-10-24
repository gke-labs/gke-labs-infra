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

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/k8s"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/repo"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	toolDocker "github.com/gke-labs/gke-labs-infra/ap/pkg/tool/docker"
	"k8s.io/klog/v2"
)

var execCommandContext = exec.CommandContext

var ensureBuildx = defaultEnsureBuildx

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
	if tag == "" {
		tag = "latest"
	}
	if imagePrefix != "" {
		return fmt.Sprintf("%s/%s:%s", imagePrefix, imageName, tag)
	}
	return fmt.Sprintf("%s:%s", imageName, tag)
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
	OnDigest     func(imageName, digest string)
}

func (t *DockerBuildTask) Run(ctx context.Context, scope *tasks.APScope) error {
	cfg, err := config.Load(scope.RepoRoot)
	if err != nil {
		return err
	}
	imagePrefix := cfg.ImageRepo()

	imagesCfg, err := config.LoadImagesConfig(scope.RepoRoot)
	if err != nil {
		return err
	}
	platforms := imagesCfg.GetPlatforms()

	tag := cfg.ImageTag()

	actualImagePrefix := imagePrefix
	if imagePrefix == "images.local" && t.Push {
		if k8s.IsInCluster() || strings.HasPrefix(t.BuildkitHost, "k8s://") {
			actualImagePrefix = "in-cluster-image-registry.in-cluster-image-registry-system.svc.cluster.local:80"
		} else {
			actualImagePrefix = "localhost:5000"
		}
	}

	fullImageName := FullImageName(actualImagePrefix, t.ImageName, tag)

	if t.BuildkitHost != "" {
		return t.runBuildctl(ctx, t.Root, fullImageName, t.Dockerfile, imagePrefix, platforms)
	}

	buildxBin := t.BuildxBin
	if buildxBin == "" && t.DockerBin != "" {
		buildxBin = t.DockerBin
	}
	if buildxBin == "" {
		var err error
		buildxBin, err = ensureBuildx(ctx, scope.RepoRoot)
		if err != nil {
			return fmt.Errorf("failed to ensure docker-buildx: %w", err)
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

	return t.runDocker(ctx, buildxBin, t.Root, fullImageName, t.Dockerfile, imagePrefix, platforms)
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

func (t *DockerBuildTask) runBuildctl(ctx context.Context, root, fullImageName, relDockerfilePath, imagePrefix string, platforms []string) error {
	klog.Infof("Building image %s from %s using buildctl", fullImageName, root)

	buildctlImageName := fullImageName
	if imagePrefix == "images.local" && t.Push && strings.HasPrefix(t.BuildkitHost, "k8s://") {
		tag := config.ImageTag()
		buildctlImageName = fmt.Sprintf("in-cluster-image-registry.in-cluster-image-registry-system.svc.cluster.local:80/%s:%s", t.ImageName, tag)
	}

	output := fmt.Sprintf("type=image,name=%s,push=%t", buildctlImageName, t.Push)
	if t.Push {
		output += ",registry.insecure=true"
	}

	tag := config.ImageTag()

	metaDir, err := os.MkdirTemp("", "ap-buildctl-meta-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir for buildctl metadata: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(metaDir); err != nil {
			klog.V(2).Infof("failed to remove temp dir: %v", err)
		}
	}()
	metaFile := filepath.Join(metaDir, "metadata.json")

	buildctlArgs := []string{
		"build",
		"--frontend", "dockerfile.v0",
		"--local", "context=.",
		"--local", "dockerfile=.",
		"--opt", "filename=" + relDockerfilePath,
		"--opt", "build-arg:IMAGE_PREFIX=" + imagePrefix,
		"--opt", "build-arg:IMAGE_TAG=" + tag,
		"--output", output,
		"--metadata-file", metaFile,
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
			Child: &tasks.DummyTask{
				Name: "run-buildctl",
				RunFn: func(ctx context.Context, scope *tasks.APScope) error {
					cmd := execCommandContext(ctx, "buildctl", buildctlArgs...)
					cmd.Dir = root
					cmd.Stdout = os.Stdout
					cmd.Stderr = os.Stderr
					cmd.Env = append(os.Environ(), "BUILDKIT_HOST=tcp://localhost:2375")
					if err := cmd.Run(); err != nil {
						return err
					}
					digest := extractDigestFromMetadataFile(metaFile)
					if digest != "" && t.OnDigest != nil {
						t.OnDigest(t.ImageName, digest)
					}
					return nil
				},
			},
			Service:    service,
			Namespace:  namespace,
			LocalPort:  2375,
			RemotePort: 2375,
		}
		return pfTask.Run(ctx, &tasks.APScope{RepoRoot: root, Dir: root})
	}

	cmd := execCommandContext(ctx, "buildctl", buildctlArgs...)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "BUILDKIT_HOST="+t.BuildkitHost)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("buildctl build failed for %s: %w", t.ImageName, err)
	}
	if t.OnDigest != nil {
		digest := extractDigestFromMetadataFile(metaFile)
		if digest != "" {
			t.OnDigest(t.ImageName, digest)
		}
	}
	return nil
}

func (t *DockerBuildTask) runDocker(ctx context.Context, buildxBin, root, fullImageName, relDockerfilePath, imagePrefix string, platforms []string) error {
	klog.Infof("Building image %s from %s using docker buildx", fullImageName, root)

	tag := config.ImageTag()

	metaDir, err := os.MkdirTemp("", "ap-buildx-meta-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir for build metadata: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(metaDir); err != nil {
			klog.V(2).Infof("failed to remove temp dir: %v", err)
		}
	}()
	metaFile := filepath.Join(metaDir, "metadata.json")

	args := []string{
		"build",
		"-t", fullImageName,
		"-f", relDockerfilePath,
		"--build-arg", "IMAGE_PREFIX=" + imagePrefix,
		"--build-arg", "IMAGE_TAG=" + tag,
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
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(buildxBin))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build failed for %s: %w", t.ImageName, err)
	}

	if t.OnDigest != nil {
		digest := extractDigestFromMetadataFile(metaFile)
		if digest == "" {
			digest = inspectImageDigest(ctx, buildxBin, fullImageName)
		}
		if digest != "" {
			t.OnDigest(t.ImageName, digest)
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
		hex := strings.TrimPrefix(id, "sha256:")
		if len(hex) >= 12 {
			return "latest-" + hex[:12]
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
func FindImageNames(r *repo.Repo, root string) ([]string, error) {
	dockerfiles, err := findDockerfiles(r, root)
	if err != nil {
		return nil, err
	}

	var names []string
	for _, dockerfile := range dockerfiles {
		relPath, err := filepath.Rel(root, dockerfile)
		if err != nil {
			continue
		}

		name := getImageName(relPath)
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// BuildTaskOptions holds optional settings for BuildTasksForImages.
type BuildTaskOptions struct {
	ImageDigests map[string]string
	OnDigest     func(imageName, digest string)
}

// BuildTasksForImages returns a task group for building only the specified docker images.
func BuildTasksForImages(r *repo.Repo, root string, push bool, buildkitHost string, imageNames []string, opts ...BuildTaskOptions) (tasks.Task, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}

	var opt BuildTaskOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	onDigest := opt.OnDigest
	if onDigest == nil && opt.ImageDigests != nil {
		onDigest = func(name, digest string) {
			opt.ImageDigests[name] = digest
		}
	}

	dockerfiles, err := findDockerfiles(r, root)
	if err != nil {
		return nil, err
	}

	filter := make(map[string]bool, len(imageNames))
	for _, name := range imageNames {
		filter[name] = true
	}

	var buildTasks []tasks.Task
	for _, dockerfile := range dockerfiles {
		relPath, err := filepath.Rel(root, dockerfile)
		if err != nil {
			continue
		}

		name := getImageName(relPath)
		if name == "" {
			continue
		}

		if !filter[name] {
			continue
		}

		buildTasks = append(buildTasks, &DockerBuildTask{
			ImageName:    name,
			Dockerfile:   relPath,
			Root:         root,
			Push:         push,
			BuildkitHost: buildkitHost,
			OnDigest:     onDigest,
		})
	}

	var rootTask tasks.Task = &tasks.Group{
		Name:  "build-images",
		Tasks: buildTasks,
	}

	if len(buildTasks) == 0 {
		return rootTask, nil
	}

	if push && cfg.ImageRepo() == "images.local" && !strings.HasPrefix(buildkitHost, "k8s://") {
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
func BuildTasks(r *repo.Repo, root string, push bool, buildkitHost string) (tasks.Task, error) {
	allImages, err := FindImageNames(r, root)
	if err != nil {
		return nil, err
	}
	return BuildTasksForImages(r, root, push, buildkitHost, allImages)
}

// Build builds docker images found in images/<name>/Dockerfile.
func Build(ctx context.Context, r *repo.Repo, root string, push bool, buildkitHost string) error {
	t, err := BuildTasks(r, root, push, buildkitHost)
	if err != nil {
		return err
	}
	return t.Run(ctx, &tasks.APScope{RepoRoot: root, Dir: root, Repo: r})
}

// HasImages returns true if there are any images to build under root.
func HasImages(r *repo.Repo, root string) (bool, error) {
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

func findDockerfiles(r *repo.Repo, root string) ([]string, error) {
	return r.Under(root).FindDockerfiles(), nil
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
