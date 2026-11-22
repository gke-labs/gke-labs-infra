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
	"crypto/sha256"
	"encoding/hex"
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
		return t.runRemoteBuildctl(ctx, scope, t.Root, fullImageName, t.Dockerfile, naming, platforms)
	}

	if t.Push && naming.Prefix == "images.local" && !naming.KindMode && !naming.InCluster {
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
	var builder string
	if t.Push && len(platforms) > 1 {
		if !t.supportsMultiPlatform(ctx, buildxBin, "") {
			if err := t.ensureMultiPlatformBuilder(ctx, buildxBin); err != nil {
				return fmt.Errorf("failed to ensure multi-platform builder: %w", err)
			}
			builder = apBuilderName
		}
		if !t.supportsMultiPlatform(ctx, buildxBin, builder) {
			return fmt.Errorf("multi-platform build (platforms: %v) is not supported for the default docker driver. "+
				"To build for multiple platforms, please switch to a different driver (e.g., docker-container, kubernetes) "+
				"or configure a single platform in '.ap/images.yaml'", platforms)
		}
	} else if !t.Push && len(platforms) > 1 {
		if !t.Quiet {
			scoped.Log(ctx).Info("building locally without pushing, falling back to native platform", "platforms", platforms)
		}
		platforms = nil
	}

	return t.runDocker(ctx, scope, buildxBin, dockerBin, t.Root, fullImageName, actualImagePrefix, t.Dockerfile, naming, platforms, builder)
}

func (t *DockerBuildTask) ensureMultiPlatformBuilder(ctx context.Context, buildxBin string) error {
	if t.supportsMultiPlatform(ctx, buildxBin, apBuilderName) {
		return nil
	}
	createCmd := execCommandContext(ctx, buildxBin, "create", "--name", apBuilderName, "--driver", "docker-container", "--driver-opt", "network=host")
	createCmd.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(buildxBin))
	if err := createCmd.Run(); err != nil {
		return fmt.Errorf("failed to create multi-platform builder: %w", err)
	}
	return nil
}

func shortHash(input string) string {
	hash := sha256.Sum256([]byte(input))
	return hex.EncodeToString(hash[:])[:12]
}

func (t *DockerBuildTask) ensureBuildkitContainer(ctx context.Context, buildxBinary string) error {
	bootstrapCommand := execCommandContext(ctx, buildxBinary, "inspect", "--bootstrap", apBuilderName)
	bootstrapCommand.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(buildxBinary))
	if err := bootstrapCommand.Run(); err != nil {
		scoped.Log(ctx).Debug("bootstrap failed, attempting to create builder", "builder", apBuilderName, "error", err)
	} else {
		return nil
	}

	createCommand := execCommandContext(ctx, buildxBinary, "create", "--name", apBuilderName, "--driver", "docker-container", "--driver-opt", "network=host")
	createCommand.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(buildxBinary))
	createError := createCommand.Run()

	secondBootstrapCommand := execCommandContext(ctx, buildxBinary, "inspect", "--bootstrap", apBuilderName)
	secondBootstrapCommand.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(buildxBinary))
	secondBootstrapError := secondBootstrapCommand.Run()
	if secondBootstrapError == nil {
		return nil
	}

	commandsRun := fmt.Sprintf("'%s inspect --bootstrap %s', '%s create --name %s --driver docker-container --driver-opt network=host', '%s inspect --bootstrap %s'",
		filepath.Base(buildxBinary), apBuilderName,
		filepath.Base(buildxBinary), apBuilderName,
		filepath.Base(buildxBinary), apBuilderName)

	return fmt.Errorf("a BuildKit container is required: failed to start builder %s (ran: %s; create error: %v, bootstrap error: %w). Please ensure Docker is running and allows running BuildKit containers (for example, privileged containers must be allowed)",
		apBuilderName, commandsRun, createError, secondBootstrapError)
}

func (t *DockerBuildTask) runLocalBuildkitPush(ctx context.Context, scope *tasks.TaskContext, root, relDockerfilePath string, naming ImageNaming, platforms []string) error {
	buildxBinary := t.BuildxBin
	if buildxBinary == "" && t.DockerBin != "" {
		buildxBinary = t.DockerBin
	}
	if buildxBinary == "" {
		var err error
		buildxBinary, err = scope.Tool(ctx, "docker-buildx")
		if err != nil {
			return fmt.Errorf("failed to ensure docker-buildx: %w", err)
		}
	}

	if err := t.ensureBuildkitContainer(ctx, buildxBinary); err != nil {
		return err
	}

	cacheRoot, err := fileutils.CacheDir()
	if err != nil {
		return fmt.Errorf("determining cache dir: %w", err)
	}
	ociDirectory := filepath.Join(cacheRoot, "images", shortHash(root), t.ImageName)
	if err := os.MkdirAll(ociDirectory, 0755); err != nil {
		return fmt.Errorf("creating OCI cache directory %s: %w", ociDirectory, err)
	}

	dockerBinary := t.dockerBinary(ctx, scope)

	invocation := buildctlInvocation{
		address:            "docker-container://" + apBuilderContainer,
		output:             "type=oci,tar=false,dest=" + ociDirectory,
		description:        apBuilderName,
		extraPathDirectory: filepath.Dir(dockerBinary),
	}

	if err := t.runBuildctl(ctx, scope, root, relDockerfilePath, invocation, naming, platforms); err != nil {
		return err
	}

	if err := pruneOCIDirectory(ctx, ociDirectory); err != nil {
		scoped.Log(ctx).Warn("failed to prune OCI cache directory", "directory", ociDirectory, "error", err)
	}

	transport, err := registrypush.NewTransport(t.Cluster, registrypush.DefaultNamespace, registrypush.DefaultService, registrypush.DefaultPort)
	if err != nil {
		return fmt.Errorf("creating registry transport for %s: %w", t.ImageName, err)
	}

	if err := registrypush.Push(ctx, transport, ociDirectory, t.ImageName, naming.Tag); err != nil {
		return fmt.Errorf("pushing %s to in-cluster registry: %w", t.ImageName, err)
	}

	return nil
}

// pruneOCIDirectory removes unreferenced blobs from an OCI layout directory
// so blobs from old builds do not accumulate without bound.
func pruneOCIDirectory(ctx context.Context, directory string) error {
	layoutPath, err := layout.FromPath(directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("opening OCI layout directory %s: %w", directory, err)
	}

	unreferencedBlobs, err := layoutPath.GarbageCollect()
	if err != nil {
		return fmt.Errorf("garbage collecting OCI layout directory %s: %w", directory, err)
	}

	for _, blobHash := range unreferencedBlobs {
		if err := layoutPath.RemoveBlob(blobHash); err != nil && !errors.Is(err, os.ErrNotExist) {
			scoped.Log(ctx).Warn("failed to remove unreferenced blob", "directory", directory, "blob", blobHash.String(), "error", err)
		}
	}

	return nil
}

func (t *DockerBuildTask) supportsMultiPlatform(ctx context.Context, buildxBin, builder string) bool {
	args := []string{"inspect"}
	if builder != "" {
		args = append(args, "--builder", builder)
	}
	cmd := execCommandContext(ctx, buildxBin, args...)
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

type buildctlInvocation struct {
	address            string
	output             string
	description        string
	extraPathDirectory string
}

func (t *DockerBuildTask) runBuildctl(ctx context.Context, scope *tasks.TaskContext, root, relDockerfilePath string, invocation buildctlInvocation, naming ImageNaming, platforms []string) error {
	buildctlBinary, err := scope.Tool(ctx, "buildctl")
	if err != nil {
		return fmt.Errorf("failed to ensure buildctl: %w", err)
	}

	start := time.Now()

	metadataFile := filepath.Join(os.TempDir(), fmt.Sprintf("buildctl-meta-%d.json", time.Now().UnixNano()))
	defer func() {
		if err := os.Remove(metadataFile); err != nil && !os.IsNotExist(err) {
			scoped.Log(ctx).Finest("failed to remove metadata file", "path", metadataFile, "error", err)
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
		"--metadata-file", metadataFile,
		"--progress=plain",
		"--output", invocation.output,
	}

	if len(platforms) > 0 {
		buildctlArgs = append(buildctlArgs, "--opt", "platform="+strings.Join(platforms, ","))
	}

	cache := setupLayerCache(ctx, t.ImageName, filepath.Join(root, relDockerfilePath))
	if cache.importArg != "" {
		buildctlArgs = append(buildctlArgs, "--import-cache", cache.importArg)
	}
	if cache.exportArg != "" {
		buildctlArgs = append(buildctlArgs, "--export-cache", cache.exportArg)
	}

	var progressBuffer bytes.Buffer

	command := execCommandContext(ctx, buildctlBinary, buildctlArgs...)
	command.Dir = root
	if scope.Stdout() == scope.Stderr() {
		merged := io.MultiWriter(scope.Stderr(), &progressBuffer)
		command.Stdout = merged
		command.Stderr = merged
	} else {
		command.Stdout = scope.Stdout()
		command.Stderr = io.MultiWriter(scope.Stderr(), &progressBuffer)
	}

	environment := fileutils.PrependPath(os.Environ(), filepath.Dir(buildctlBinary))
	if invocation.extraPathDirectory != "" {
		environment = fileutils.PrependPath(environment, invocation.extraPathDirectory)
	}
	command.Env = append(environment, "BUILDKIT_HOST="+invocation.address)

	if err := command.Run(); err != nil {
		if cache.stagingDirectory != "" {
			if rmErr := os.RemoveAll(cache.stagingDirectory); rmErr != nil && !os.IsNotExist(rmErr) {
				scoped.Log(ctx).Warn("failed to clean staging directory", "dir", cache.stagingDirectory, "error", rmErr)
			}
		}
		return fmt.Errorf("buildctl build failed for %s: %w", t.ImageName, err)
	}

	cache.evaluate(ctx, t.ImageName, &progressBuffer)

	digest := extractDigestFromMetadataFile(metadataFile)
	if naming.Tag == "latest" && digest != "" {
		ref := naming.Ref(t.ImageName)
		ref.Digest = digest
	}

	if !t.Quiet {
		contextSize := parseContextSizeFromPlain(&progressBuffer)
		duration := time.Since(start)
		kvs := []any{"image", t.ImageName, "builder", invocation.description, "duration", formatDuration(duration)}
		if contextSize != "" {
			kvs = append(kvs, "sent", contextSize)
		}
		scoped.Log(ctx).Info("built image", kvs...)
	}

	return nil
}

func (t *DockerBuildTask) runRemoteBuildctl(ctx context.Context, scope *tasks.TaskContext, root, fullImageName, relDockerfilePath string, naming ImageNaming, platforms []string) (retErr error) {
	var output string
	var archivePath string
	if t.Load {
		temporaryArchive, err := fileutils.CreateTempFile(filepath.Join(os.TempDir(), "ap-buildctl-image.tar"))
		if err != nil {
			return fmt.Errorf("creating temporary archive for %s: %w", t.ImageName, err)
		}
		defer temporaryArchive.Cleanup(ctx)
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

	invocation := buildctlInvocation{
		address:     buildkitHost,
		output:      output,
		description: t.BuildkitHost,
	}

	if err := t.runBuildctl(ctx, scope, root, relDockerfilePath, invocation, naming, platforms); err != nil {
		return err
	}

	if t.Load {
		dockerBinary := t.dockerBinary(ctx, scope)
		loadCommand := execCommandContext(ctx, dockerBinary, "load", "-i", archivePath)
		loadCommand.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(dockerBinary))
		loadOutput, err := loadCommand.CombinedOutput()
		if err != nil {
			return fmt.Errorf("loading built image %s: %w (%s)", fullImageName, err, strings.TrimSpace(string(loadOutput)))
		}
	}

	return nil
}

func (t *DockerBuildTask) runDocker(ctx context.Context, scope *tasks.TaskContext, buildxBin, dockerBin, root, fullImageName, actualImagePrefix, relDockerfilePath string, naming ImageNaming, platforms []string, builder string) error {
	start := time.Now()

	metaFile := filepath.Join(os.TempDir(), fmt.Sprintf("buildx-meta-%d.json", time.Now().UnixNano()))
	defer func() {
		if err := os.Remove(metaFile); err != nil && !os.IsNotExist(err) {
			scoped.Log(ctx).Finest("failed to remove metadata file", "path", metaFile, "error", err)
		}
	}()

	args := []string{
		"build",
		"--progress=plain",
		"-t", fullImageName,
		"-f", relDockerfilePath,
		"--build-arg", "IMAGE_PREFIX=" + naming.Prefix,
		"--build-arg", "IMAGE_TAG=" + naming.Tag,
		"--metadata-file", metaFile,
	}

	if builder != "" {
		args = append(args, "--builder", builder)
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
	cache := setupLayerCache(ctx, t.ImageName, filepath.Join(root, relDockerfilePath))
	if cache.importArg != "" {
		args = append(args, "--cache-from", cache.importArg)
	}
	if cache.exportArg != "" {
		args = append(args, "--cache-to", cache.exportArg)
	}
	args = append(args, ".")

	var progressBuffer bytes.Buffer
	cmd := execCommandContext(ctx, buildxBin, args...)
	cmd.Dir = root
	if scope.Stdout() == scope.Stderr() {
		merged := io.MultiWriter(scope.Stderr(), &progressBuffer)
		cmd.Stdout = merged
		cmd.Stderr = merged
	} else {
		cmd.Stdout = scope.Stdout()
		cmd.Stderr = io.MultiWriter(scope.Stderr(), &progressBuffer)
	}
	cmd.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(buildxBin))
	if err := cmd.Run(); err != nil {
		if cache.stagingDirectory != "" {
			if rmErr := os.RemoveAll(cache.stagingDirectory); rmErr != nil && !os.IsNotExist(rmErr) {
				scoped.Log(ctx).Warn("failed to clean staging directory", "dir", cache.stagingDirectory, "error", rmErr)
			}
		}
		return fmt.Errorf("docker build failed for %s: %w", t.ImageName, err)
	}

	duration := time.Since(start)

	cache.evaluate(ctx, t.ImageName, &progressBuffer)

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
		scoped.Log(ctx).Info("built image", "image", t.ImageName, "builder", "docker", "duration", formatDuration(duration))
	}

	return nil
}

const layerCacheDirectoryName = "ap-layer-cache"

type imageCacheMetadata struct {
	DockerfileHash string             `json:"dockerfileHash"`
	ExportDuration float64            `json:"exportDurationSeconds"`
	StepDurations  map[string]float64 `json:"stepDurations"`
	Dropped        bool               `json:"dropped,omitempty"`
}

type layerCache struct {
	importArg        string
	exportArg        string
	finalDirectory   string
	stagingDirectory string
	dockerfileHash   string
	hasExistingMeta  bool
	existingMeta     imageCacheMetadata
}

func getLayerCacheRoot() string {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return ""
	}
	runnerTemp := os.Getenv("RUNNER_TEMP")
	if runnerTemp == "" {
		return ""
	}
	return filepath.Join(runnerTemp, layerCacheDirectoryName)
}

func hashDockerfile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func setupLayerCache(ctx context.Context, imageName, dockerfilePath string) layerCache {
	cacheRoot := getLayerCacheRoot()
	if cacheRoot == "" {
		return layerCache{}
	}

	finalDirectory := filepath.Join(cacheRoot, imageName)
	stagingDirectory := filepath.Join(cacheRoot, imageName+"-stage")
	metaFile := filepath.Join(finalDirectory, "metadata.json")
	dockerfileHash := hashDockerfile(dockerfilePath)

	var existingMeta imageCacheMetadata
	hasExistingMeta := false
	data, err := os.ReadFile(metaFile)
	if err == nil {
		if unmarshalErr := json.Unmarshal(data, &existingMeta); unmarshalErr == nil {
			hasExistingMeta = true
		} else {
			scoped.Log(ctx).Warn("failed to parse metadata file", "path", metaFile, "error", unmarshalErr)
		}
	} else if !os.IsNotExist(err) {
		scoped.Log(ctx).Warn("failed to read metadata file", "path", metaFile, "error", err)
	}

	if hasExistingMeta && existingMeta.Dropped && existingMeta.DockerfileHash == dockerfileHash {
		return layerCache{}
	}

	var importArg string
	if _, statErr := os.Stat(filepath.Join(finalDirectory, "index.json")); statErr == nil {
		importArg = "type=local,src=" + finalDirectory
		scoped.Log(ctx).Info("restoring layer cache", "image", imageName, "src", finalDirectory)
	} else if !os.IsNotExist(statErr) {
		scoped.Log(ctx).Warn("failed to check layer cache index", "dir", finalDirectory, "error", statErr)
	}

	var exportArg string
	if isCacheSaveAllowed() {
		exportArg = fmt.Sprintf("type=local,dest=%s,mode=max,compression=uncompressed", stagingDirectory)
		if err := os.RemoveAll(stagingDirectory); err != nil && !os.IsNotExist(err) {
			scoped.Log(ctx).Warn("failed to clean staging layer cache directory", "dir", stagingDirectory, "error", err)
		}
		if err := os.MkdirAll(stagingDirectory, 0755); err != nil {
			scoped.Log(ctx).Warn("failed to create staging layer cache directory", "dir", stagingDirectory, "error", err)
		}
	}

	return layerCache{
		importArg:        importArg,
		exportArg:        exportArg,
		finalDirectory:   finalDirectory,
		stagingDirectory: stagingDirectory,
		dockerfileHash:   dockerfileHash,
		hasExistingMeta:  hasExistingMeta,
		existingMeta:     existingMeta,
	}
}

func (c layerCache) evaluate(ctx context.Context, imageName string, progress io.Reader) {
	if c.exportArg == "" {
		return
	}
	stats := parseBuildKitProgress(progress)
	exportDuration := stats.exportDuration
	if exportDuration <= 0 {
		exportDuration = 5.0
	}

	if !c.hasExistingMeta || len(c.existingMeta.StepDurations) == 0 {
		meta := imageCacheMetadata{
			DockerfileHash: c.dockerfileHash,
			ExportDuration: exportDuration,
			StepDurations:  stats.stepDurations,
		}
		data, marshalErr := json.MarshalIndent(meta, "", "  ")
		if marshalErr != nil {
			scoped.Log(ctx).Warn("failed to marshal metadata", "error", marshalErr)
			return
		}
		if writeErr := os.WriteFile(filepath.Join(c.stagingDirectory, "metadata.json"), data, 0644); writeErr != nil {
			scoped.Log(ctx).Warn("failed to write metadata file", "error", writeErr)
			return
		}
		if rmErr := os.RemoveAll(c.finalDirectory); rmErr != nil && !os.IsNotExist(rmErr) {
			scoped.Log(ctx).Warn("failed to clean final directory", "dir", c.finalDirectory, "error", rmErr)
		}
		if renameErr := os.Rename(c.stagingDirectory, c.finalDirectory); renameErr != nil {
			scoped.Log(ctx).Warn("failed to promote staging directory", "error", renameErr)
			return
		}
		scoped.Log(ctx).Info("evaluated layer cache", "image", imageName, "saved", "0s", "cost", formatDuration(time.Duration(exportDuration*float64(time.Second))), "action", "kept")
		finalizeLayerCache(ctx)
		return
	}

	var savedSeconds float64
	for step := range stats.cachedSteps {
		if d, ok := c.existingMeta.StepDurations[step]; ok {
			savedSeconds += d
		}
	}
	costSeconds := c.existingMeta.ExportDuration + 5.0

	if savedSeconds <= costSeconds {
		if rmErr := os.RemoveAll(c.stagingDirectory); rmErr != nil && !os.IsNotExist(rmErr) {
			scoped.Log(ctx).Warn("failed to clean staging directory", "dir", c.stagingDirectory, "error", rmErr)
		}
		if rmErr := os.RemoveAll(c.finalDirectory); rmErr != nil && !os.IsNotExist(rmErr) {
			scoped.Log(ctx).Warn("failed to clean final directory", "dir", c.finalDirectory, "error", rmErr)
		}
		if mkdirErr := os.MkdirAll(c.finalDirectory, 0755); mkdirErr != nil {
			scoped.Log(ctx).Warn("failed to create final directory", "dir", c.finalDirectory, "error", mkdirErr)
			return
		}
		droppedMeta := imageCacheMetadata{
			DockerfileHash: c.dockerfileHash,
			Dropped:        true,
		}
		data, marshalErr := json.MarshalIndent(droppedMeta, "", "  ")
		if marshalErr != nil {
			scoped.Log(ctx).Warn("failed to marshal metadata", "error", marshalErr)
			return
		}
		if writeErr := os.WriteFile(filepath.Join(c.finalDirectory, "metadata.json"), data, 0644); writeErr != nil {
			scoped.Log(ctx).Warn("failed to write metadata file", "error", writeErr)
			return
		}
		scoped.Log(ctx).Info("evaluated layer cache", "image", imageName, "saved", formatDuration(time.Duration(savedSeconds*float64(time.Second))), "cost", formatDuration(time.Duration(costSeconds*float64(time.Second))), "action", "dropped")
		finalizeLayerCache(ctx)
		return
	}

	hasNewSteps := false
	for step := range stats.stepDurations {
		if !stats.cachedSteps[step] {
			hasNewSteps = true
			break
		}
	}

	if hasNewSteps {
		if c.existingMeta.StepDurations == nil {
			c.existingMeta.StepDurations = make(map[string]float64)
		}
		for k, v := range stats.stepDurations {
			c.existingMeta.StepDurations[k] = v
		}
		c.existingMeta.ExportDuration = exportDuration
		c.existingMeta.DockerfileHash = c.dockerfileHash
		data, marshalErr := json.MarshalIndent(c.existingMeta, "", "  ")
		if marshalErr != nil {
			scoped.Log(ctx).Warn("failed to marshal metadata", "error", marshalErr)
			return
		}
		if writeErr := os.WriteFile(filepath.Join(c.stagingDirectory, "metadata.json"), data, 0644); writeErr != nil {
			scoped.Log(ctx).Warn("failed to write metadata file", "error", writeErr)
			return
		}
		if rmErr := os.RemoveAll(c.finalDirectory); rmErr != nil && !os.IsNotExist(rmErr) {
			scoped.Log(ctx).Warn("failed to clean final directory", "dir", c.finalDirectory, "error", rmErr)
		}
		if renameErr := os.Rename(c.stagingDirectory, c.finalDirectory); renameErr != nil {
			scoped.Log(ctx).Warn("failed to promote staging directory", "error", renameErr)
			return
		}
	} else {
		if rmErr := os.RemoveAll(c.stagingDirectory); rmErr != nil && !os.IsNotExist(rmErr) {
			scoped.Log(ctx).Warn("failed to clean staging directory", "dir", c.stagingDirectory, "error", rmErr)
		}
	}
	scoped.Log(ctx).Info("evaluated layer cache", "image", imageName, "saved", formatDuration(time.Duration(savedSeconds*float64(time.Second))), "cost", formatDuration(time.Duration(costSeconds*float64(time.Second))), "action", "kept")
	finalizeLayerCache(ctx)
}

func finalizeLayerCache(ctx context.Context) {
	if !isCacheSaveAllowed() {
		return
	}
	cacheRoot := getLayerCacheRoot()
	if cacheRoot == "" {
		return
	}

	entries, err := os.ReadDir(cacheRoot)
	if err != nil {
		return
	}

	h := sha256.New()
	hasAnyMetadata := false

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		imageDir := filepath.Join(cacheRoot, entry.Name())
		metaPath := filepath.Join(imageDir, "metadata.json")
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		hasAnyMetadata = true
		h.Write([]byte(entry.Name()))
		h.Write(data)
	}

	if !hasAnyMetadata {
		return
	}

	hash := hex.EncodeToString(h.Sum(nil))
	if len(hash) > 16 {
		hash = hash[:16]
	}
	runnerOS := os.Getenv("RUNNER_OS")
	if runnerOS == "" {
		runnerOS = "Linux"
	}
	key := fmt.Sprintf("ap-layer-cache-%s-%s", runnerOS, hash)
	if err := setGitHubActionsEnv(ctx, "AP_LAYER_CACHE_KEY", key); err != nil {
		scoped.Log(ctx).Warn("failed to set AP_LAYER_CACHE_KEY in GITHUB_ENV", "error", err)
	}
	scoped.Log(ctx).Info("layer cache finalized", "key", key)
}

func setGitHubActionsEnv(ctx context.Context, name, value string) error {
	githubEnv := os.Getenv("GITHUB_ENV")
	if githubEnv == "" {
		return nil
	}
	f, err := os.OpenFile(githubEnv, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	_, printErr := fmt.Fprintf(f, "%s=%s\n", name, value)
	closeErr := f.Close()
	if printErr != nil {
		return printErr
	}
	return closeErr
}

func isCacheSaveAllowed() bool {
	return os.Getenv("GITHUB_ACTIONS") == "true" && os.Getenv("GITHUB_EVENT_NAME") == "push"
}

type buildStepStats struct {
	stepDurations  map[string]float64
	cachedSteps    map[string]bool
	exportDuration float64
}

func isBuildStep(desc string) bool {
	if !strings.HasPrefix(desc, "[") {
		return false
	}
	if strings.HasPrefix(desc, "[internal]") || strings.HasPrefix(desc, "[auth]") {
		return false
	}
	return true
}

func parseBuildKitProgress(r io.Reader) buildStepStats {
	scanner := bufio.NewScanner(r)
	stepDescriptions := make(map[string]string)
	stepDurations := make(map[string]float64)
	cachedSteps := make(map[string]bool)
	var exportDuration float64

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line[1:], " ", 2)
		if len(parts) < 2 {
			continue
		}
		vertexID := parts[0]
		rest := strings.TrimSpace(parts[1])

		if strings.HasPrefix(rest, "[") || strings.Contains(rest, "exporting cache") {
			stepDescriptions[vertexID] = rest
		} else if rest == "CACHED" || strings.HasPrefix(rest, "CACHED ") {
			if desc, ok := stepDescriptions[vertexID]; ok {
				cachedSteps[desc] = true
			}
		} else if strings.HasPrefix(rest, "DONE ") {
			fields := strings.Fields(rest)
			if len(fields) >= 2 {
				durStr := fields[1]
				dur, parseErr := time.ParseDuration(durStr)
				if parseErr != nil {
					continue
				}
				sec := dur.Seconds()
				if desc, ok := stepDescriptions[vertexID]; ok {
					if strings.Contains(desc, "exporting cache") {
						exportDuration = sec
					} else if isBuildStep(desc) {
						stepDurations[desc] = sec
					}
				}
			}
		}
	}

	return buildStepStats{
		stepDurations:  stepDurations,
		cachedSteps:    cachedSteps,
		exportDuration: exportDuration,
	}
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
