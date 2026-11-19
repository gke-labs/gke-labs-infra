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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/k8s"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/registrypush"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
)

var execCommandContext = exec.CommandContext

const (
	apBuilderName      = "ap-builder"
	apBuilderContainer = "buildx_buildkit_ap-builder0"
)

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
	Cluster      k8s.KubeCluster
	Quiet        bool
	Load         bool
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

	if t.Push && naming.Prefix == "images.local" && !naming.KindMode {
		return t.runLocalBuildkitPush(ctx, scope, t.Root, t.Dockerfile, naming, platforms)
	}

	buildxBin := t.BuildxBin
	if buildxBin == "" && t.DockerBin != "" {
		buildxBin = t.DockerBin
	}
	if buildxBin == "" {
		var err error
		buildxBin, err = scope.Tool(ctx, "docker-buildx")
		if err != nil {
			return fmt.Errorf("failed to ensure docker-buildx: %w", err)
		}
	}

	dockerBin := t.dockerBinary(ctx, scope)

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
		if !t.Quiet {
			scoped.Log(ctx).Info("building locally without pushing, falling back to native platform", "platforms", platforms)
		}
		platforms = nil
	}

	return t.runDocker(ctx, scope, buildxBin, dockerBin, t.Root, fullImageName, actualImagePrefix, t.Dockerfile, naming, platforms)
}

func (t *DockerBuildTask) ensureMultiPlatformBuilder(ctx context.Context, buildxBin string) error {
	if t.supportsMultiPlatform(ctx, buildxBin) {
		return nil
	}
	// Try to use existing ap-builder
	useCmd := execCommandContext(ctx, buildxBin, "use", apBuilderName)
	useCmd.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(buildxBin))
	if err := useCmd.Run(); err == nil && t.supportsMultiPlatform(ctx, buildxBin) {
		return nil
	}
	// Create new ap-builder with docker-container driver and host networking
	createCmd := execCommandContext(ctx, buildxBin, "create", "--name", apBuilderName, "--driver", "docker-container", "--driver-opt", "network=host", "--use")
	createCmd.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(buildxBin))
	if err := createCmd.Run(); err != nil {
		return fmt.Errorf("failed to create multi-platform builder: %w", err)
	}
	return nil
}

func (t *DockerBuildTask) ensureBuildkitContainer(ctx context.Context, scope *tasks.TaskContext, dockerBin, buildxBin string) error {
	var attempts []string

	// 1. Check if the container is already running
	inspectCmd := execCommandContext(ctx, dockerBin, "inspect", "-f", "{{.State.Running}}", apBuilderContainer)
	inspectCmd.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(dockerBin))
	out, err := inspectCmd.Output()
	if err == nil && strings.TrimSpace(string(out)) == "true" {
		return nil
	}

	// 2. If container exists but is not running, try starting it
	if err == nil && strings.TrimSpace(string(out)) == "false" {
		attempts = append(attempts, fmt.Sprintf("docker start %s", apBuilderContainer))
		startCmd := execCommandContext(ctx, dockerBin, "start", apBuilderContainer)
		startCmd.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(dockerBin))
		if startErr := startCmd.Run(); startErr != nil {
			scoped.Log(ctx).Debug("docker start failed", "container", apBuilderContainer, "error", startErr)
		} else {
			checkCmd := execCommandContext(ctx, dockerBin, "inspect", "-f", "{{.State.Running}}", apBuilderContainer)
			checkCmd.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(dockerBin))
			if chkOut, chkErr := checkCmd.Output(); chkErr != nil {
				scoped.Log(ctx).Debug("docker inspect running check failed", "container", apBuilderContainer, "error", chkErr)
			} else if strings.TrimSpace(string(chkOut)) == "true" {
				return nil
			}
		}
	}

	// 3. Container does not exist or start failed; ensure builder exists and bootstrap it
	useCmd := execCommandContext(ctx, buildxBin, "use", apBuilderName)
	useCmd.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(buildxBin))
	if err := useCmd.Run(); err != nil {
		attempts = append(attempts, fmt.Sprintf("docker-buildx create --name %s --driver docker-container --driver-opt network=host --use", apBuilderName))
		createCmd := execCommandContext(ctx, buildxBin, "create", "--name", apBuilderName, "--driver", "docker-container", "--driver-opt", "network=host", "--use")
		createCmd.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(buildxBin))
		if createErr := createCmd.Run(); createErr != nil {
			return fmt.Errorf("a BuildKit container is required: failed to create builder %s (tried %s): %w. Please ensure Docker is running and allows running BuildKit containers (for example, privileged containers must be allowed)",
				apBuilderName, strings.Join(attempts, ", "), createErr)
		}
	}

	attempts = append(attempts, fmt.Sprintf("docker-buildx inspect --bootstrap %s", apBuilderName))
	bootCmd := execCommandContext(ctx, buildxBin, "inspect", "--bootstrap", apBuilderName)
	bootCmd.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(buildxBin))
	if bootErr := bootCmd.Run(); bootErr != nil {
		return fmt.Errorf("a BuildKit container is required: failed to start builder %s (tried %s): %w. Please ensure Docker is running and allows running BuildKit containers (for example, privileged containers must be allowed)",
			apBuilderName, strings.Join(attempts, ", "), bootErr)
	}

	// Verify it is running
	checkCmd := execCommandContext(ctx, dockerBin, "inspect", "-f", "{{.State.Running}}", apBuilderContainer)
	checkCmd.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(dockerBin))
	chkOut, chkErr := checkCmd.Output()
	if chkErr != nil || strings.TrimSpace(string(chkOut)) != "true" {
		return fmt.Errorf("a BuildKit container is required: BuildKit container %s is not running (tried %s). Please ensure Docker is running and allows running BuildKit containers (for example, privileged containers must be allowed)",
			apBuilderContainer, strings.Join(attempts, ", "))
	}

	return nil
}

func (t *DockerBuildTask) runLocalBuildkitPush(ctx context.Context, scope *tasks.TaskContext, root, relDockerfilePath string, naming ImageNaming, platforms []string) error {
	start := time.Now()

	buildctlBin, err := scope.Tool(ctx, "buildctl")
	if err != nil {
		return fmt.Errorf("failed to ensure buildctl: %w", err)
	}

	dockerBin := t.dockerBinary(ctx, scope)

	buildxBin := t.BuildxBin
	if buildxBin == "" && t.DockerBin != "" {
		buildxBin = t.DockerBin
	}
	if buildxBin == "" {
		var err error
		buildxBin, err = scope.Tool(ctx, "docker-buildx")
		if err != nil {
			return fmt.Errorf("failed to ensure docker-buildx: %w", err)
		}
	}

	if err := t.ensureBuildkitContainer(ctx, scope, dockerBin, buildxBin); err != nil {
		return err
	}

	cacheRoot, err := fileutils.CacheDir()
	if err != nil {
		return fmt.Errorf("determining cache dir: %w", err)
	}
	ociDir := filepath.Join(cacheRoot, "images", t.ImageName)
	if err := os.MkdirAll(ociDir, 0755); err != nil {
		return fmt.Errorf("creating OCI cache directory %s: %w", ociDir, err)
	}

	metaFile := filepath.Join(os.TempDir(), fmt.Sprintf("buildctl-meta-%d.json", time.Now().UnixNano()))
	defer func() {
		if err := os.Remove(metaFile); err != nil && !os.IsNotExist(err) {
			scoped.Log(ctx).Finest("failed to remove metadata file", "path", metaFile, "error", err)
		}
	}()

	buildctlArgs := []string{
		"--addr", "docker-container://" + apBuilderContainer,
		"build",
		"--frontend", "dockerfile.v0",
		"--local", "context=.",
		"--local", "dockerfile=.",
		"--opt", "filename=" + relDockerfilePath,
		"--opt", "build-arg:IMAGE_PREFIX=" + naming.Prefix,
		"--opt", "build-arg:IMAGE_TAG=" + naming.Tag,
		"--metadata-file", metaFile,
		"--progress=plain",
		"--output", "type=oci,tar=false,dest=" + ociDir,
	}

	if len(platforms) > 0 {
		buildctlArgs = append(buildctlArgs, "--opt", "platform="+strings.Join(platforms, ","))
	}

	var progressBuf bytes.Buffer
	cmd := execCommandContext(ctx, buildctlBin, buildctlArgs...)
	cmd.Dir = root
	cmd.Stdout = scope.Stdout()
	cmd.Stderr = io.MultiWriter(scope.Stderr(), &progressBuf)
	env := fileutils.PrependPath(os.Environ(), filepath.Dir(dockerBin))
	env = fileutils.PrependPath(env, filepath.Dir(buildctlBin))
	if buildxBin != "" {
		env = fileutils.PrependPath(env, filepath.Dir(buildxBin))
	}
	cmd.Env = env

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("buildctl build failed for %s: %w", t.ImageName, err)
	}

	if err := pruneOCIDirectory(ctx, ociDir); err != nil {
		scoped.Log(ctx).Warn("failed to prune OCI cache directory", "dir", ociDir, "error", err)
	}

	transport, err := registrypush.NewTransport(t.Cluster, registrypush.DefaultNamespace, registrypush.DefaultService, registrypush.DefaultPort)
	if err != nil {
		return fmt.Errorf("creating registry transport for %s: %w", t.ImageName, err)
	}

	if err := registrypush.Push(ctx, transport, ociDir, t.ImageName, naming.Tag); err != nil {
		return fmt.Errorf("pushing %s to in-cluster registry: %w", t.ImageName, err)
	}

	digest := extractDigestFromMetadataFile(metaFile)
	if naming.Tag == "latest" && digest != "" {
		ref := naming.Ref(t.ImageName)
		ref.Digest = digest
	}

	if !t.Quiet {
		contextSize := parseContextSizeFromPlain(&progressBuf)
		duration := time.Since(start)
		if contextSize != "" {
			scoped.Log(ctx).Info(fmt.Sprintf("built %s with %s in %s; sent %s", t.ImageName, apBuilderName, formatDuration(duration), contextSize))
		} else {
			scoped.Log(ctx).Info(fmt.Sprintf("built %s with %s in %s", t.ImageName, apBuilderName, formatDuration(duration)))
		}
	}

	return nil
}

// pruneOCIDirectory removes unreferenced blobs from an OCI layout directory
// so blobs from old builds do not accumulate without bound.
func pruneOCIDirectory(ctx context.Context, dir string) error {
	layoutPath, err := layout.FromPath(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("opening OCI layout directory %s: %w", dir, err)
	}

	index, err := layoutPath.ImageIndex()
	if err != nil {
		return fmt.Errorf("reading image index from %s: %w", dir, err)
	}

	referencedDigests := make(map[string]bool)

	var scanIndex func(idx v1.ImageIndex) error
	scanIndex = func(idx v1.ImageIndex) error {
		im, err := idx.IndexManifest()
		if err != nil {
			return err
		}
		for _, manifestDesc := range im.Manifests {
			referencedDigests[manifestDesc.Digest.String()] = true
			if manifestDesc.MediaType.IsIndex() {
				childIndex, err := idx.ImageIndex(manifestDesc.Digest)
				if err != nil {
					return err
				}
				if err := scanIndex(childIndex); err != nil {
					return err
				}
			} else {
				img, err := idx.Image(manifestDesc.Digest)
				if err != nil {
					img, err = layoutPath.Image(manifestDesc.Digest)
				}
				if err != nil {
					return err
				}
				manifest, err := img.Manifest()
				if err != nil {
					return err
				}
				referencedDigests[manifest.Config.Digest.String()] = true
				for _, layer := range manifest.Layers {
					referencedDigests[layer.Digest.String()] = true
				}
			}
		}
		return nil
	}

	if err := scanIndex(index); err != nil {
		return fmt.Errorf("scanning OCI layout digests in %s: %w", dir, err)
	}

	blobsDir := filepath.Join(dir, "blobs")
	entries, err := os.ReadDir(blobsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		algo := entry.Name()
		algoDir := filepath.Join(blobsDir, algo)
		blobFiles, err := os.ReadDir(algoDir)
		if err != nil {
			continue
		}
		for _, bf := range blobFiles {
			if bf.IsDir() {
				continue
			}
			digestStr := fmt.Sprintf("%s:%s", algo, bf.Name())
			if !referencedDigests[digestStr] {
				filePath := filepath.Join(algoDir, bf.Name())
				if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
					scoped.Log(ctx).Fine("failed to remove unreferenced blob", "path", filePath, "error", err)
				}
			}
		}
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

func (t *DockerBuildTask) dockerBinary(ctx context.Context, scope *tasks.TaskContext) string {
	if t.DockerBin != "" {
		return t.DockerBin
	}
	if dockerPath, err := scope.Tool(ctx, "docker"); err == nil && dockerPath != "" {
		return dockerPath
	}
	return "docker"
}

func (t *DockerBuildTask) runBuildctl(ctx context.Context, scope *tasks.TaskContext, root, fullImageName, relDockerfilePath string, naming ImageNaming, platforms []string) (retErr error) {
	buildctlBin, err := scope.Tool(ctx, "buildctl")
	if err != nil {
		return fmt.Errorf("failed to ensure buildctl: %w", err)
	}

	start := time.Now()

	var output string
	var archivePath string
	if t.Load {
		temporaryArchive, err := fileutils.CreateTempFile(filepath.Join(os.TempDir(), "ap-buildctl-image.tar"))
		if err != nil {
			return fmt.Errorf("creating temporary archive for %s: %w", t.ImageName, err)
		}
		defer temporaryArchive.Cleanup()
		if err := temporaryArchive.Close(); err != nil {
			return fmt.Errorf("closing temporary archive for %s: %w", t.ImageName, err)
		}
		archivePath = temporaryArchive.Name()
		output = fmt.Sprintf("type=docker,name=%s,dest=%s", fullImageName, archivePath)
	} else {
		output = fmt.Sprintf("type=image,name=%s,push=%t", fullImageName, t.Push)
		if t.Push {
			output += ",registry.insecure=true"
		}
	}

	metaFile := filepath.Join(os.TempDir(), fmt.Sprintf("buildctl-meta-%d.json", time.Now().UnixNano()))
	defer func() {
		if err := os.Remove(metaFile); err != nil && !os.IsNotExist(err) {
			scoped.Log(ctx).Finest("failed to remove metadata file", "path", metaFile, "error", err)
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
		"--progress=plain",
		"--output", output,
	}

	if len(platforms) > 0 {
		buildctlArgs = append(buildctlArgs, "--opt", "platform="+strings.Join(platforms, ","))
	}

	var progressBuf bytes.Buffer

	buildkitHost := t.BuildkitHost
	if host, ok := strings.CutPrefix(t.BuildkitHost, "k8s://"); ok {
		parts := strings.Split(host, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("invalid buildkit host: %s, expected k8s://namespace/service", t.BuildkitHost)
		}
		namespace, service := parts[0], parts[1]

		if k8s.IsInCluster() {
			buildkitHost = fmt.Sprintf("tcp://%s.%s.svc:2375", service, namespace)
		} else {
			forwarder, err := k8s.NewBuildkitForwarder(ctx, t.Cluster, namespace, service)
			if err != nil {
				return err
			}
			defer func() {
				if closeErr := forwarder.Close(); closeErr != nil {
					retErr = errors.Join(retErr, closeErr)
				}
			}()
			buildkitHost = "unix://" + forwarder.SocketPath()
		}
	}

	cmd := execCommandContext(ctx, buildctlBin, buildctlArgs...)
	cmd.Dir = root
	cmd.Stdout = scope.Stdout()
	cmd.Stderr = io.MultiWriter(scope.Stderr(), &progressBuf)
	cmd.Env = append(fileutils.PrependPath(os.Environ(), filepath.Dir(buildctlBin)), "BUILDKIT_HOST="+buildkitHost)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("buildctl build failed for %s: %w", t.ImageName, err)
	}

	if t.Load {
		dockerBin := t.dockerBinary(ctx, scope)
		loadCommand := execCommandContext(ctx, dockerBin, "load", "-i", archivePath)
		loadCommand.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(dockerBin))
		loadOutput, err := loadCommand.CombinedOutput()
		if err != nil {
			return fmt.Errorf("loading built image %s: %w (%s)", fullImageName, err, strings.TrimSpace(string(loadOutput)))
		}
	}

	digest := extractDigestFromMetadataFile(metaFile)
	if naming.Tag == "latest" && digest != "" {
		ref := naming.Ref(t.ImageName)
		ref.Digest = digest
	}

	if !t.Quiet {
		contextSize := parseContextSizeFromPlain(&progressBuf)
		duration := time.Since(start)
		if contextSize != "" {
			scoped.Log(ctx).Info(fmt.Sprintf("built %s with %s in %s; sent %s", t.ImageName, t.BuildkitHost, formatDuration(duration), contextSize))
		} else {
			scoped.Log(ctx).Info(fmt.Sprintf("built %s with %s in %s", t.ImageName, t.BuildkitHost, formatDuration(duration)))
		}
	}

	return nil
}

func (t *DockerBuildTask) runDocker(ctx context.Context, scope *tasks.TaskContext, buildxBin, dockerBin, root, fullImageName, actualImagePrefix, relDockerfilePath string, naming ImageNaming, platforms []string) error {
	start := time.Now()

	metaFile := filepath.Join(os.TempDir(), fmt.Sprintf("buildx-meta-%d.json", time.Now().UnixNano()))
	defer func() {
		if err := os.Remove(metaFile); err != nil && !os.IsNotExist(err) {
			scoped.Log(ctx).Finest("failed to remove metadata file", "path", metaFile, "error", err)
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

	if !t.Quiet {
		duration := time.Since(start)
		scoped.Log(ctx).Info(fmt.Sprintf("built %s with docker in %s", t.ImageName, formatDuration(duration)))
	}

	return nil
}

// parseContextSizeFromPlain parses a buildctl --progress=plain stream and returns
// the transfer figure from the last "transferring context: <size> done" line of
// the "[internal] load build context" vertex (e.g. "44.26kB"). If no transfer line
// is found, it returns "".
func parseContextSizeFromPlain(r io.Reader) string {
	scanner := bufio.NewScanner(r)
	var contextVertexID string
	var lastSize string
	inBuildContext := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.Contains(line, "[internal] load build context") {
			inBuildContext = true
			if strings.HasPrefix(line, "#") {
				fields := strings.Fields(line)
				if len(fields) > 0 {
					contextVertexID = fields[0]
				}
			}
			continue
		}

		if strings.Contains(line, "transferring context:") {
			matches := false
			if contextVertexID != "" {
				matches = strings.HasPrefix(line, contextVertexID+" ") || strings.HasPrefix(line, contextVertexID+"\t")
			} else if inBuildContext {
				matches = true
			}
			if matches {
				idx := strings.Index(line, "transferring context:")
				rest := strings.TrimSpace(line[idx+len("transferring context:"):])
				fields := strings.Fields(rest)
				if len(fields) > 0 {
					lastSize = fields[0]
				}
			}
		}
	}

	return lastSize
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(100 * time.Millisecond).String()
}

type buildMetadataFile struct {
	Digest string `json:"containerimage.digest"`
}

func extractDigestFromMetadataFile(metaPath string) string {
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return ""
	}
	var metadata buildMetadataFile
	if err := json.Unmarshal(data, &metadata); err != nil {
		return ""
	}
	return strings.TrimSpace(metadata.Digest)
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
	if t.BuildkitHost != "" {
		return fmt.Sprintf("docker-build-%s (buildkit: %s)", t.ImageName, t.BuildkitHost)
	}
	return fmt.Sprintf("docker-build-%s", t.ImageName)
}

func (t *DockerBuildTask) GetChildren() []tasks.Task {
	return nil
}

func (t *DockerBuildTask) SourcePath() string {
	return filepath.Join(t.Root, t.Dockerfile)
}

var _ tasks.Sourced = (*DockerBuildTask)(nil)

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
func BuildTasksForImages(r *sourcetree.SourceTree, root string, push bool, buildkitHost string, imageNames []string, naming ImageNaming, cluster k8s.KubeCluster) (tasks.Task, error) {
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
			Cluster:      cluster,
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

	return rootTask, nil
}

// BuildTasks returns a task group for building all docker images found in images/<name>/Dockerfile.
func BuildTasks(r *sourcetree.SourceTree, root string, push bool, buildkitHost string, naming ImageNaming) (tasks.Task, error) {
	allImages, err := FindImageNames(r, root)
	if err != nil {
		return nil, err
	}
	return BuildTasksForImages(r, root, push, buildkitHost, allImages, naming, k8s.CurrentCluster())
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
