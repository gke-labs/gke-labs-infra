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

package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/images"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins/api"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins/stdio"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// StartPluginFunc launches or connects to a plugin and returns a gRPC client and closer.
type StartPluginFunc func(ctx context.Context, scope *tasks.TaskContext, imageRef string, allowNetwork bool, stderr io.Writer) (api.PluginServiceClient, io.Closer, error)

// StartDockerPlugin launches a plugin container using docker run -i --rm.
func StartDockerPlugin(ctx context.Context, scope *tasks.TaskContext, imageRef string, allowNetwork bool, stderr io.Writer) (api.PluginServiceClient, io.Closer, error) {
	dockerPath, err := scope.Tool(ctx, "docker")
	if err != nil {
		return nil, nil, fmt.Errorf("plugin requires a container runtime (docker): %w", err)
	}

	commandArguments := []string{"run", "-i", "--rm"}
	if !allowNetwork {
		commandArguments = append(commandArguments, "--network", "none")
	}
	commandArguments = append(commandArguments, imageRef)

	command := exec.CommandContext(ctx, dockerPath, commandArguments...)
	command.Stderr = stderr
	stdinPipe, err := command.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("creating stdin pipe: %w", err)
	}
	stdoutPipe, err := command.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("creating stdout pipe: %w", err)
	}
	if err := command.Start(); err != nil {
		return nil, nil, fmt.Errorf("starting plugin container (%s): %w", imageRef, err)
	}

	commandConnection := stdio.NewCommandConn(command, stdinPipe, stdoutPipe)
	return DialStdioConn(ctx, commandConnection)
}

// describeTimeout is the deadline for a plugin to respond to the Describe RPC.
const describeTimeout = 30 * time.Second

// DialStdioConn establishes a gRPC client connection over the given net.Conn.
func DialStdioConn(ctx context.Context, connection net.Conn) (api.PluginServiceClient, io.Closer, error) {
	grpcClientConnection, err := grpc.NewClient("passthrough:///stdio",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// gRPC's default minimum connect timeout is 20s, which would expire before
		// describeTimeout (30s) and produce a premature "error reading server preface"
		// error. Ensure connect timeout exceeds describeTimeout.
		grpc.WithConnectParams(grpc.ConnectParams{
			MinConnectTimeout: describeTimeout + 5*time.Second,
		}),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return connection, nil
		}),
	)
	if err != nil {
		if closeError := connection.Close(); closeError != nil {
			scoped.Log(ctx).Debug("failed to close connection on dial error", "error", closeError)
		}
		return nil, nil, fmt.Errorf("connecting to plugin stdio: %w", err)
	}

	client := api.NewPluginServiceClient(grpcClientConnection)
	closer := closerFunc(func() error {
		return errors.Join(grpcClientConnection.Close(), connection.Close())
	})
	return client, closer, nil
}

type closerFunc func() error

func (closer closerFunc) Close() error {
	return closer()
}

// BuildPluginImage builds a Dockerfile plugin image using ap's DockerBuildTask and returns its image ID.
func BuildPluginImage(ctx context.Context, scope *tasks.TaskContext, pluginName, dockerfilePath, buildkitHost string) (string, error) {
	naming := images.ImageNaming{
		Prefix: "ap-plugin",
		Tag:    "latest",
	}
	task := &images.DockerBuildTask{
		ImageName:    pluginName,
		Dockerfile:   dockerfilePath,
		Root:         scope.RepoRoot(),
		BuildkitHost: buildkitHost,
		Quiet:        true,
		Load:         true,
		Naming:       naming,
	}

	var outputBuffer bytes.Buffer
	taskScope := scope.WithOutput(&outputBuffer, &outputBuffer)
	if err := task.Run(ctx, taskScope); err != nil {
		buildOutput := strings.TrimSpace(outputBuffer.String())
		if buildOutput != "" {
			return "", fmt.Errorf("building plugin %q with Dockerfile %s: %w\n%s", pluginName, dockerfilePath, err, buildOutput)
		}
		return "", fmt.Errorf("building plugin %q with Dockerfile %s: %w", pluginName, dockerfilePath, err)
	}

	dockerBin, err := scope.Tool(ctx, "docker")
	if err != nil {
		return "", fmt.Errorf("plugin %q requires a container runtime (docker): %w", pluginName, err)
	}

	fullImageName := images.ImageRef{Prefix: naming.Prefix, Name: pluginName, Tag: naming.Tag}.String()
	imageID, err := inspectImageID(ctx, dockerBin, fullImageName)
	if err != nil {
		return "", fmt.Errorf("reading image ID for plugin %q (Dockerfile %s): %w", pluginName, dockerfilePath, err)
	}
	return imageID, nil
}

func inspectImageID(ctx context.Context, dockerBin, fullImageName string) (string, error) {
	command := exec.CommandContext(ctx, dockerBin, "image", "inspect", "--format", "{{.Id}}", fullImageName)
	command.Env = fileutils.PrependPath(os.Environ(), filepath.Dir(dockerBin))
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("inspecting image ID for %s: %w", fullImageName, err)
	}
	imageID := strings.TrimSpace(string(output))
	if imageID == "" {
		return "", fmt.Errorf("empty image ID for %s", fullImageName)
	}
	return imageID, nil
}

// DescribeContainer runs Describe RPC on the plugin container on cold runs and caches the manifest.
func DescribeContainer(ctx context.Context, scope *tasks.TaskContext, cacheDirectory, pluginName, imageRef string, starter StartPluginFunc) (*PluginManifest, error) {
	key := CacheKey(imageRef)
	manifest, err := LoadCachedManifest(cacheDirectory, key)
	if err == nil {
		return manifest, nil
	}

	describeContext, cancel := context.WithTimeout(ctx, describeTimeout)
	defer cancel()

	var stderrBuffer bytes.Buffer
	client, closer, err := starter(describeContext, scope, imageRef, false, &stderrBuffer)
	if err != nil {
		if isDeadlineExceeded(describeContext, err) {
			return nil, fmt.Errorf("plugin %q (%s) did not answer Describe within %v", pluginName, imageRef, describeTimeout)
		}
		return nil, fmt.Errorf("starting plugin %q (%s) for describe: %w", pluginName, imageRef, err)
	}
	defer func() {
		if closeError := closer.Close(); closeError != nil {
			scoped.Log(ctx).Debug("failed to close plugin client during describe", "error", closeError)
		}
	}()

	response, err := client.Describe(describeContext, &api.DescribeRequest{})
	if err != nil {
		if isDeadlineExceeded(describeContext, err) {
			return nil, fmt.Errorf("plugin %q (%s) did not answer Describe within %v", pluginName, imageRef, describeTimeout)
		}
		stderrOutput := strings.TrimSpace(stderrBuffer.String())
		if stderrOutput != "" {
			return nil, fmt.Errorf("running describe for plugin %q (%s): %w (%s)", pluginName, imageRef, err, stderrOutput)
		}
		return nil, fmt.Errorf("running describe for plugin %q (%s): %w", pluginName, imageRef, err)
	}

	parsedManifest := &PluginManifest{
		APIVersion: "ap.plugin/v1alpha1",
		Name:       response.Name,
		Activation: response.Activation,
		Network:    response.Network,
		Tasks:      make([]TaskMetadata, len(response.Tasks)),
	}
	for index, taskMetadata := range response.Tasks {
		parsedManifest.Tasks[index] = TaskMetadata{
			Name:        taskMetadata.Name,
			Verb:        taskMetadata.Verb,
			Description: taskMetadata.Description,
			Outputs:     taskMetadata.Outputs,
		}
	}

	output, err := json.MarshalIndent(parsedManifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling plugin %q manifest: %w", pluginName, err)
	}

	sanitizedKey := strings.ReplaceAll(strings.ReplaceAll(key, ":", "-"), "/", "-")
	manifestPath := filepath.Join(cacheDirectory, "plugins", "describe", sanitizedKey+".json")
	temporaryFile, err := fileutils.CreateTempFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("creating temp file for %s: %w", manifestPath, err)
	}
	defer temporaryFile.Cleanup()

	if _, err := temporaryFile.Write(output); err != nil {
		return nil, fmt.Errorf("writing cached manifest %s: %w", manifestPath, err)
	}
	if err := temporaryFile.Rename(0644); err != nil {
		return nil, fmt.Errorf("saving cached manifest %s: %w", manifestPath, err)
	}

	return parsedManifest, nil
}

// isDeadlineExceeded reports whether an operation failed because the describe deadline passed,
// either via the context deadline or via a gRPC DeadlineExceeded status code.
func isDeadlineExceeded(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return status.Code(err) == codes.DeadlineExceeded
}

// ContainerPlugin implements Plugin for container-based plugins.
type ContainerPlugin struct {
	Name        string
	Declaration config.PluginDeclaration
	ImageRef    string
	Manifest    *PluginManifest
	RepoRoot    string
	Starter     StartPluginFunc
}

// Describe returns the manifest describing the container plugin.
func (plugin *ContainerPlugin) Describe(ctx context.Context) (*PluginManifest, error) {
	return plugin.Manifest, nil
}

// DiscoverTasks discovers tasks for the container plugin under directory.
func (plugin *ContainerPlugin) DiscoverTasks(ctx context.Context, directory string, scope *tasks.TaskContext) ([]PluginTask, error) {
	activatedDirectories, err := FindActivatedDirs(scope.SourceTree().Under(directory), plugin.Name, plugin.Manifest.Activation)
	if err != nil {
		return nil, err
	}

	var discoveredTasks []PluginTask
	for _, activatedDirectory := range activatedDirectories {
		relativeDirectory, err := filepath.Rel(plugin.RepoRoot, activatedDirectory)
		if err != nil {
			return nil, fmt.Errorf("plugin %q: calculating relative directory for %s: %w", plugin.Name, activatedDirectory, err)
		}
		relativeDirectory = filepath.ToSlash(relativeDirectory)

		for _, taskMetadata := range plugin.Manifest.Tasks {
			containerTask := &ContainerTask{
				PluginName:        plugin.Name,
				TaskMetadata:      taskMetadata,
				Declaration:       plugin.Declaration,
				ImageRef:          plugin.ImageRef,
				Manifest:          plugin.Manifest,
				Dir:               activatedDirectory,
				RelativeDirectory: relativeDirectory,
				RepoRoot:          plugin.RepoRoot,
				Starter:           plugin.Starter,
			}
			discoveredTasks = append(discoveredTasks, PluginTask{
				Plugin:       plugin.Name,
				TaskMetadata: taskMetadata,
				Task:         containerTask,
			})
		}
	}
	return discoveredTasks, nil
}

// ContainerPluginOptions configures container plugin loading, image building, and caching.
type ContainerPluginOptions struct {
	// CacheDirectory is the root cache directory (e.g. AP_CACHE_DIR) for plugin manifests and build records.
	CacheDirectory string

	// BuildkitHost is the address of an optional remote BuildKit daemon (e.g. BUILDKIT_HOST).
	BuildkitHost string

	// Starter starts a plugin container process and establishes a gRPC client connection.
	Starter StartPluginFunc

	// BuildImage builds a Dockerfile plugin image and returns its resulting image ID.
	BuildImage BuildPluginImageFunc

	// ImageLookup checks whether a built container image is present in the local container engine.
	ImageLookup ImageLookupFunc
}

// LoadContainerPlugins loads all enabled container plugins configured in .ap/plugins.yaml
// using the provided options.
func LoadContainerPlugins(ctx context.Context, scope *tasks.TaskContext, options ContainerPluginOptions) ([]Plugin, error) {
	pluginsConfig, err := config.LoadPluginsConfig(scope.RepoRoot())
	if err != nil {
		return nil, err
	}
	if len(pluginsConfig.Plugins) == 0 {
		return nil, nil
	}

	var pluginNames []string
	for name := range pluginsConfig.Plugins {
		pluginNames = append(pluginNames, name)
	}
	sort.Strings(pluginNames)

	var result []Plugin
	for _, name := range pluginNames {
		declaration := pluginsConfig.Plugins[name]
		if !declaration.IsEnabled() {
			continue
		}
		imageRef := declaration.Image
		if declaration.Dockerfile != "" {
			var buildError error
			imageRef, buildError = EnsureDockerfilePluginImage(ctx, scope, name, declaration.Dockerfile, options)
			if buildError != nil {
				return nil, buildError
			}
		}
		manifest, err := DescribeContainer(ctx, scope, options.CacheDirectory, name, imageRef, options.Starter)
		if err != nil {
			return nil, err
		}
		result = append(result, &ContainerPlugin{
			Name:        name,
			Declaration: declaration,
			ImageRef:    imageRef,
			Manifest:    manifest,
			RepoRoot:    scope.RepoRoot(),
			Starter:     options.Starter,
		})
	}
	return result, nil
}

// ContainerTask represents a task executed inside a container plugin.
type ContainerTask struct {
	PluginName        string
	TaskMetadata      TaskMetadata
	Declaration       config.PluginDeclaration
	ImageRef          string
	Manifest          *PluginManifest
	Dir               string
	RelativeDirectory string
	RepoRoot          string
	Starter           StartPluginFunc
}

var (
	_ tasks.Task    = (*ContainerTask)(nil)
	_ tasks.Sourced = (*ContainerTask)(nil)
	_ tasks.Outputs = (*ContainerTask)(nil)
)

// GetName returns the hyphenated task name `<plugin>-<task>`, suffixed by the activating directory.
func (task *ContainerTask) GetName() string {
	name := fmt.Sprintf("%s-%s", task.PluginName, task.TaskMetadata.Name)
	if task.RelativeDirectory == "." {
		return name
	}
	return fmt.Sprintf("%s %s", name, task.RelativeDirectory)
}

// GetChildren returns no children for leaf container tasks.
func (task *ContainerTask) GetChildren() []tasks.Task {
	return nil
}

// SourcePath returns the activated directory path for task filtering.
func (task *ContainerTask) SourcePath() string {
	return task.Dir
}

// Outputs returns the paths this task generates as absolute paths.
// Implements tasks.Outputs.
func (task *ContainerTask) Outputs() []string {
	var absolutePaths []string
	for _, output := range task.TaskMetadata.Outputs {
		if output != "." && output != "" {
			absolutePaths = append(absolutePaths, filepath.Clean(filepath.Join(task.Dir, output)))
		}
	}
	return absolutePaths
}

// Run executes the task in a container sandbox via gRPC over stdio.
func (task *ContainerTask) Run(ctx context.Context, scope *tasks.TaskContext) error {
	if err := task.run(ctx, scope); err != nil {
		return fmt.Errorf("plugin %q task %q in %s: %w", task.PluginName, task.TaskMetadata.Name, task.Dir, err)
	}
	return nil
}

func (task *ContainerTask) run(ctx context.Context, scope *tasks.TaskContext) error {
	if task.Manifest.Network && !task.Declaration.Network {
		return fmt.Errorf("requires network access, but network is not authorized in .ap/plugins.yaml")
	}

	allowNetwork := task.Manifest.Network && task.Declaration.Network

	client, closer, err := task.Starter(ctx, scope, task.ImageRef, allowNetwork, scope.Stderr())
	if err != nil {
		return err
	}
	defer func() {
		if closeError := closer.Close(); closeError != nil {
			scoped.Log(ctx).Debug("failed to close plugin client", "error", closeError)
		}
	}()

	streamContext, cancelStream := context.WithCancel(ctx)
	defer cancelStream()

	stream, err := client.RunTask(streamContext)
	if err != nil {
		return fmt.Errorf("opening task stream: %w", err)
	}

	stagingDirectory, err := os.MkdirTemp("", "ap-plugin-stage-*")
	if err != nil {
		return fmt.Errorf("creating staging directory: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(stagingDirectory); removeErr != nil && !os.IsNotExist(removeErr) {
			scoped.Log(ctx).Debug("failed to remove staging directory", "dir", stagingDirectory, "error", removeErr)
		}
	}()

	stagingRoot, err := fileutils.OpenUntrustedRoot(stagingDirectory)
	if err != nil {
		return fmt.Errorf("opening staging root %s: %w", stagingDirectory, err)
	}
	defer func() {
		if closeError := stagingRoot.Close(); closeError != nil {
			scoped.Log(ctx).Debug("failed to close staging root", "error", closeError)
		}
	}()

	receiveDone := make(chan error, 1)
	go func() {
		receiveDone <- task.receiveOutputs(stream, stagingRoot, scope)
	}()

	if sendError := task.sendInputs(streamContext, stream, scope); sendError != nil {
		cancelStream()
		<-receiveDone
		return sendError
	}

	if receiveError := <-receiveDone; receiveError != nil {
		return receiveError
	}

	if scope.Verify() {
		return verifyStagedOutputs(ctx, task.Dir, stagingDirectory, task.TaskMetadata.Outputs, scope.Stderr())
	}

	return placeStagedOutputs(task.Dir, stagingDirectory, task.TaskMetadata.Outputs, scope.SourceTree())
}

func (task *ContainerTask) sendInputs(ctx context.Context, stream api.PluginService_RunTaskClient, scope *tasks.TaskContext) error {
	startRequest := &api.RunTaskRequest{
		Payload: &api.RunTaskRequest_Start{
			Start: &api.TaskStart{
				Task: task.TaskMetadata.Name,
				Dir:  task.RelativeDirectory,
			},
		},
	}
	if err := stream.Send(startRequest); err != nil {
		return fmt.Errorf("sending task start: %w", err)
	}

	repositoryTree := scope.SourceTree().RepoTree()
	repositoryRoot, err := os.OpenRoot(task.RepoRoot)
	if err != nil {
		return fmt.Errorf("opening repository root %s: %w", task.RepoRoot, err)
	}
	defer func() {
		if closeError := repositoryRoot.Close(); closeError != nil {
			scoped.Log(ctx).Debug("failed to close repository root", "error", closeError)
		}
	}()

	resolvedRepositoryRoot, err := filepath.EvalSymlinks(task.RepoRoot)
	if err != nil {
		return fmt.Errorf("resolving repository root %s: %w", task.RepoRoot, err)
	}

	const maximumChunkSize = 1024 * 1024 // 1 MB

	for _, file := range repositoryTree.Files() {
		relativeFilePath, err := filepath.Rel(task.RepoRoot, file.Path)
		if err != nil {
			return fmt.Errorf("calculating relative path for %s: %w", file.Path, err)
		}
		relativeFilePath = filepath.ToSlash(relativeFilePath)

		lstatInfo, err := repositoryRoot.Lstat(relativeFilePath)
		if err != nil {
			return fmt.Errorf("stating file %s: %w", relativeFilePath, err)
		}

		if lstatInfo.Mode()&os.ModeSymlink != 0 {
			resolvedTarget, err := filepath.EvalSymlinks(file.Path)
			if err != nil {
				return fmt.Errorf("resolving symlink %s: %w", relativeFilePath, err)
			}
			targetRelativePath, err := filepath.Rel(resolvedRepositoryRoot, resolvedTarget)
			if err != nil || targetRelativePath == ".." || strings.HasPrefix(targetRelativePath, ".."+string(filepath.Separator)) || filepath.IsAbs(targetRelativePath) {
				return fmt.Errorf("file %s resolves to %s which is outside the repository view or ignored", relativeFilePath, resolvedTarget)
			}
			targetInRepositoryView := filepath.Join(task.RepoRoot, targetRelativePath)
			if targetFile := repositoryTree.File(targetInRepositoryView); targetFile == nil {
				return fmt.Errorf("file %s resolves to %s which is outside the repository view or ignored", relativeFilePath, resolvedTarget)
			}
		}

		openedFile, err := repositoryRoot.Open(relativeFilePath)
		if err != nil {
			return fmt.Errorf("opening file %s: %w", relativeFilePath, err)
		}

		fileContent, fileMode, err := readFileContentAndMode(openedFile, relativeFilePath)
		if closeError := openedFile.Close(); closeError != nil {
			scoped.Log(ctx).Debug("failed to close file", "path", relativeFilePath, "error", closeError)
		}
		if err != nil {
			return err
		}

		if len(fileContent) == 0 {
			request := &api.RunTaskRequest{
				Payload: &api.RunTaskRequest_File{
					File: &api.FileChunk{
						Path:        relativeFilePath,
						Mode:        fileMode,
						Content:     nil,
						IsLastChunk: true,
					},
				},
			}
			if err := stream.Send(request); err != nil {
				return fmt.Errorf("sending file %s: %w", relativeFilePath, err)
			}
		} else {
			for offset := 0; offset < len(fileContent); offset += maximumChunkSize {
				end := offset + maximumChunkSize
				isLastChunk := false
				if end >= len(fileContent) {
					end = len(fileContent)
					isLastChunk = true
				}
				request := &api.RunTaskRequest{
					Payload: &api.RunTaskRequest_File{
						File: &api.FileChunk{
							Path:        relativeFilePath,
							Mode:        fileMode,
							Content:     fileContent[offset:end],
							IsLastChunk: isLastChunk,
						},
					},
				}
				if err := stream.Send(request); err != nil {
					return fmt.Errorf("sending file %s: %w", relativeFilePath, err)
				}
			}
		}
	}

	if err := stream.CloseSend(); err != nil {
		return fmt.Errorf("closing send stream: %w", err)
	}
	return nil
}

func readFileContentAndMode(openedFile *os.File, relativeFilePath string) ([]byte, uint32, error) {
	fileInfo, err := openedFile.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("stating file %s: %w", relativeFilePath, err)
	}

	if !fileInfo.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("file %s is not a regular file", relativeFilePath)
	}

	fileContent, err := io.ReadAll(openedFile)
	if err != nil {
		return nil, 0, fmt.Errorf("reading file %s: %w", relativeFilePath, err)
	}

	return fileContent, uint32(fileInfo.Mode().Perm()), nil
}

func (task *ContainerTask) receiveOutputs(stream api.PluginService_RunTaskClient, stagingRoot *fileutils.UntrustedRoot, scope *tasks.TaskContext) error {
	var currentFilePath string
	var currentFileBuffer bytes.Buffer
	var currentFileMode uint32
	var taskResult *api.TaskResult
	const maximumFileSize = 100 * 1024 * 1024 // 100 MB per file limit

	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("stream error: %w", err)
		}

		switch payload := response.Payload.(type) {
		case *api.RunTaskResponse_Log:
			logLine := payload.Log
			if logLine.Stream == api.LogLine_STDERR {
				if _, writeError := scope.Stderr().Write(logLine.Data); writeError != nil {
					return fmt.Errorf("writing stderr: %w", writeError)
				}
			} else {
				if _, writeError := scope.Stdout().Write(logLine.Data); writeError != nil {
					return fmt.Errorf("writing stdout: %w", writeError)
				}
			}
		case *api.RunTaskResponse_OutputFile:
			chunk := payload.OutputFile
			if currentFilePath == "" {
				currentFilePath = chunk.Path
				currentFileMode = chunk.Mode
				currentFileBuffer.Reset()
			} else if currentFilePath != chunk.Path {
				return fmt.Errorf("received chunk for %q before completing %q", chunk.Path, currentFilePath)
			}

			currentFileBuffer.Write(chunk.Content)
			if int64(currentFileBuffer.Len()) > maximumFileSize {
				return fmt.Errorf("output file %q exceeds 100 MB limit", currentFilePath)
			}

			if chunk.IsLastChunk {
				fileMode := os.FileMode(currentFileMode).Perm()
				if writeError := stagingRoot.WriteFile(currentFilePath, currentFileBuffer.Bytes(), fileMode); writeError != nil {
					return fmt.Errorf("writing output file %s: %w", currentFilePath, writeError)
				}
				currentFilePath = ""
				currentFileBuffer.Reset()
			}
		case *api.RunTaskResponse_Result:
			taskResult = payload.Result
		}
	}

	if currentFilePath != "" {
		return fmt.Errorf("stream ended with incomplete chunks for %q", currentFilePath)
	}

	if taskResult == nil {
		return fmt.Errorf("plugin exited early without returning a result")
	}

	if taskResult.ExitCode != 0 {
		errorMessage := strings.TrimSpace(taskResult.ErrorMessage)
		if errorMessage != "" {
			return fmt.Errorf("task failed with exit code %d: %s", taskResult.ExitCode, errorMessage)
		}
		return fmt.Errorf("task failed with exit code %d", taskResult.ExitCode)
	}

	return nil
}

func isCoLocated(outputs []string) bool {
	return len(outputs) == 1 && (outputs[0] == "." || filepath.Clean(outputs[0]) == ".")
}

func placeStagedOutputs(targetDirectory, stagingDirectory string, outputs []string, tree *sourcetree.SourceTree) (returnError error) {
	untrustedRoot, err := fileutils.OpenUntrustedRoot(targetDirectory)
	if err != nil {
		return fmt.Errorf("opening root %s: %w", targetDirectory, err)
	}
	defer func() {
		if closeError := untrustedRoot.Close(); closeError != nil && returnError == nil {
			returnError = closeError
		}
	}()

	if err := untrustedRoot.CopyStagedOutputs(stagingDirectory, outputs); err != nil {
		return fmt.Errorf("placing plugin outputs into %s: %w", targetDirectory, err)
	}

	if invalidateErr := tree.InvalidateAll(); invalidateErr != nil {
		return fmt.Errorf("invalidating source tree: %w", invalidateErr)
	}
	return nil
}

func verifyStagedOutputs(ctx context.Context, targetDirectory, stagingDirectory string, outputs []string, outputWriter io.Writer) error {
	var stagedFiles []string
	if walkError := fileutils.WalkStagedOutputs(stagingDirectory, outputs, func(path string, info os.FileInfo, stagingRoot *os.Root) error {
		stagedFiles = append(stagedFiles, path)
		return nil
	}); walkError != nil {
		return walkError
	}

	var diffOutputs [][]byte

	if isCoLocated(outputs) {
		for _, relativePath := range stagedFiles {
			checkoutPath := filepath.Join(targetDirectory, relativePath)
			if _, statError := os.Stat(checkoutPath); os.IsNotExist(statError) {
				checkoutPath = "/dev/null"
			}
			stagedPath := filepath.Join(stagingDirectory, relativePath)
			diffOutput, hasDiff, err := gitDiff(ctx, checkoutPath, stagedPath)
			if err != nil {
				return err
			}
			if hasDiff {
				diffOutputs = append(diffOutputs, diffOutput)
			}
		}
	} else {
		for _, outputEntry := range outputs {
			cleanEntry := filepath.Clean(outputEntry)
			checkoutPath := filepath.Join(targetDirectory, cleanEntry)
			var cleanupDirectory string
			if _, statError := os.Stat(checkoutPath); os.IsNotExist(statError) {
				emptyDirectory, err := os.MkdirTemp("", "ap-empty-*")
				if err != nil {
					return fmt.Errorf("creating empty directory for diff: %w", err)
				}
				cleanupDirectory = emptyDirectory
				checkoutPath = emptyDirectory
			}
			stagedPath := filepath.Join(stagingDirectory, cleanEntry)
			diffOutput, hasDiff, err := gitDiff(ctx, checkoutPath, stagedPath)
			if cleanupDirectory != "" {
				if removeErr := os.RemoveAll(cleanupDirectory); removeErr != nil && !os.IsNotExist(removeErr) {
					scoped.Log(ctx).Debug("failed to remove temporary directory", "dir", cleanupDirectory, "error", removeErr)
				}
			}
			if err != nil {
				return err
			}
			if hasDiff {
				diffOutputs = append(diffOutputs, diffOutput)
			}
		}
	}

	if len(diffOutputs) > 0 {
		for _, diffOutput := range diffOutputs {
			if _, writeError := outputWriter.Write(diffOutput); writeError != nil {
				return fmt.Errorf("writing diff: %w", writeError)
			}
		}
		return fmt.Errorf("outputs differ from the checkout; run 'ap generate' to update")
	}

	return nil
}

func gitDiff(ctx context.Context, leftPath, rightPath string) ([]byte, bool, error) {
	command := exec.CommandContext(ctx, "git", "--no-pager", "diff", "--no-index", "--no-color", leftPath, rightPath)
	var environment []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_CONFIG_") {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, "GIT_CONFIG_GLOBAL=", "GIT_CONFIG_NOSYSTEM=1")
	command.Env = environment

	outputBytes, err := command.CombinedOutput()
	if err == nil {
		return nil, false, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return outputBytes, true, nil
	}
	return outputBytes, false, fmt.Errorf("running git diff: %w (%s)", err, strings.TrimSpace(string(outputBytes)))
}
