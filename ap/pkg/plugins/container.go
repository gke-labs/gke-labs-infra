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
	"archive/tar"
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
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool"
)

// DescribeContainer runs `<image> describe` in a container on cold runs and caches the manifest.
func DescribeContainer(ctx context.Context, repoRoot, cacheDirectory, pluginName string, declaration config.PluginDeclaration) (*PluginManifest, error) {
	key := CacheKey(declaration)
	manifest, err := LoadCachedManifest(cacheDirectory, key)
	if err == nil {
		return manifest, nil
	}

	dockerTool, ok := tool.Lookup("docker")
	if !ok {
		return nil, fmt.Errorf("plugin %q requires a container runtime: docker tool not registered", pluginName)
	}
	dockerPath, err := dockerTool.Ensure(ctx, repoRoot)
	if err != nil {
		return nil, fmt.Errorf("plugin %q requires a container runtime (docker): %w", pluginName, err)
	}

	command := exec.CommandContext(ctx, dockerPath, "run", "--rm", "--network", "none", declaration.Image, "describe")
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("running describe for plugin %q: %w (%s)", pluginName, err, strings.TrimSpace(string(output)))
	}

	var parsedManifest PluginManifest
	if err := json.Unmarshal(output, &parsedManifest); err != nil {
		return nil, fmt.Errorf("parsing plugin %q manifest: %w", pluginName, err)
	}

	sanitizedKey := strings.ReplaceAll(strings.ReplaceAll(key, ":", "-"), "/", "-")
	manifestPath := filepath.Join(cacheDirectory, "plugins", "describe", sanitizedKey+".json")
	tempFile, err := fileutils.CreateTempFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("creating temp file for %s: %w", manifestPath, err)
	}
	defer tempFile.Cleanup()

	if _, err := tempFile.Write(output); err != nil {
		return nil, fmt.Errorf("writing cached manifest %s: %w", manifestPath, err)
	}
	if err := tempFile.Rename(0644); err != nil {
		return nil, fmt.Errorf("saving cached manifest %s: %w", manifestPath, err)
	}

	return &parsedManifest, nil
}

// ContainerPlugin implements Plugin for container-based plugins.
type ContainerPlugin struct {
	Name        string
	Declaration config.PluginDeclaration
	Manifest    *PluginManifest
	RepoRoot    string
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
	for _, activatedDir := range activatedDirectories {
		for _, taskMetadata := range plugin.Manifest.Tasks {
			containerTask := &ContainerTask{
				PluginName:   plugin.Name,
				TaskMetadata: taskMetadata,
				Declaration:  plugin.Declaration,
				Manifest:     plugin.Manifest,
				Dir:          activatedDir,
				RepoRoot:     plugin.RepoRoot,
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

// LoadContainerPlugins loads all configured container plugins from .ap/plugins.yaml.
func LoadContainerPlugins(ctx context.Context, repoRoot, cacheDirectory string) ([]Plugin, error) {
	pluginsConfig, err := config.LoadPluginsConfig(repoRoot)
	if err != nil {
		return nil, err
	}
	if pluginsConfig == nil || len(pluginsConfig.Plugins) == 0 {
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
		manifest, err := DescribeContainer(ctx, repoRoot, cacheDirectory, name, declaration)
		if err != nil {
			return nil, err
		}
		result = append(result, &ContainerPlugin{
			Name:        name,
			Declaration: declaration,
			Manifest:    manifest,
			RepoRoot:    repoRoot,
		})
	}
	return result, nil
}

// ContainerTask represents a task executed inside a container plugin.
type ContainerTask struct {
	PluginName   string
	TaskMetadata TaskMetadata
	Declaration  config.PluginDeclaration
	Manifest     *PluginManifest
	Dir          string
	RepoRoot     string
}

var (
	_ tasks.Task    = (*ContainerTask)(nil)
	_ tasks.Sourced = (*ContainerTask)(nil)
)

// GetName returns the hyphenated task name `<plugin>-<task>`.
func (task *ContainerTask) GetName() string {
	return fmt.Sprintf("%s-%s", task.PluginName, task.TaskMetadata.Name)
}

// GetChildren returns no children for leaf container tasks.
func (task *ContainerTask) GetChildren() []tasks.Task {
	return nil
}

// SourcePath returns the activated directory path for task filtering.
func (task *ContainerTask) SourcePath() string {
	return task.Dir
}

// Run executes the task in a container sandbox.
func (task *ContainerTask) Run(ctx context.Context, scope *tasks.TaskContext) error {
	if task.Manifest.Network && !task.Declaration.Network {
		return fmt.Errorf("plugin %q requires network access, but network is not authorized in .ap/plugins.yaml", task.PluginName)
	}

	dockerPath, err := scope.Tool(ctx, "docker")
	if err != nil {
		return fmt.Errorf("plugin %q in %s requires a container runtime (docker): %w", task.PluginName, task.Dir, err)
	}

	relativeDir, err := filepath.Rel(scope.RepoRoot(), task.Dir)
	if err != nil {
		return fmt.Errorf("calculating relative directory for %s: %w", task.Dir, err)
	}
	relativeDir = filepath.ToSlash(relativeDir)

	var createArguments []string
	createArguments = append(createArguments, "create")
	if !task.Manifest.Network || !task.Declaration.Network {
		createArguments = append(createArguments, "--network", "none")
	}
	createArguments = append(createArguments, task.Declaration.Image, "run", task.TaskMetadata.Name, "--src", "/src", "--dir", relativeDir, "--out", "/out")

	createCommand := exec.CommandContext(ctx, dockerPath, createArguments...)
	createOutput, err := createCommand.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("creating container for plugin %q: %w (%s)", task.PluginName, err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return fmt.Errorf("creating container for plugin %q: %w", task.PluginName, err)
	}
	containerID := strings.TrimSpace(string(createOutput))

	defer func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if output, removeErr := exec.CommandContext(cleanupContext, dockerPath, "rm", "-f", containerID).CombinedOutput(); removeErr != nil {
			scope.Log().V(2).Info("failed to remove plugin container", "container", containerID, "error", removeErr, "output", strings.TrimSpace(string(output)))
		}
	}()

	pipeReader, pipeWriter := io.Pipe()
	copyInCommand := exec.CommandContext(ctx, dockerPath, "cp", "-", containerID+":/")
	copyInCommand.Stdin = pipeReader

	tarErrorChannel := make(chan error, 1)
	go func() {
		tarError := writeTarStream(scope.SourceTree().RepoTree(), scope.RepoRoot(), pipeWriter)
		_ = pipeWriter.CloseWithError(tarError)
		tarErrorChannel <- tarError
	}()

	copyInOutput, copyInErr := copyInCommand.CombinedOutput()
	_ = pipeReader.CloseWithError(copyInErr)

	tarError := <-tarErrorChannel
	if copyInErr != nil {
		return fmt.Errorf("copying source into plugin container %s: %w (%s)", containerID, copyInErr, strings.TrimSpace(string(copyInOutput)))
	}
	if tarError != nil && !errors.Is(tarError, io.ErrClosedPipe) {
		return fmt.Errorf("creating source archive for plugin %q: %w", task.PluginName, tarError)
	}

	startCommand := exec.CommandContext(ctx, dockerPath, "start", "-a", containerID)
	startCommand.Stdout = scope.Stdout()
	startCommand.Stderr = scope.Stderr()
	if err := startCommand.Run(); err != nil {
		return fmt.Errorf("plugin %q task %q failed: %w", task.PluginName, task.TaskMetadata.Name, err)
	}

	if len(task.TaskMetadata.Outputs) == 0 {
		return nil
	}

	stagingDirectory, err := os.MkdirTemp("", "ap-plugin-stage-*")
	if err != nil {
		return fmt.Errorf("creating staging directory: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(stagingDirectory); removeErr != nil && !os.IsNotExist(removeErr) {
			scope.Log().V(2).Info("failed to remove staging directory", "dir", stagingDirectory, "error", removeErr)
		}
	}()

	copyOutCommand := exec.CommandContext(ctx, dockerPath, "cp", containerID+":/out/.", stagingDirectory)
	if copyOutOutput, err := copyOutCommand.CombinedOutput(); err != nil {
		return fmt.Errorf("copying output from container %s: %w (%s)", containerID, err, strings.TrimSpace(string(copyOutOutput)))
	}

	return placeStagedOutputs(task.Dir, stagingDirectory, task.TaskMetadata.Outputs, scope.SourceTree())
}

func isCoLocated(outputs []string) bool {
	return len(outputs) == 1 && outputs[0] == "."
}

func writeTarStream(tree *sourcetree.SourceTree, repositoryRoot string, outputWriter io.Writer) (returnError error) {
	tarWriter := tar.NewWriter(outputWriter)
	defer func() {
		if closeErr := tarWriter.Close(); closeErr != nil && returnError == nil {
			returnError = closeErr
		}
	}()

	outDirectoryHeader := &tar.Header{
		Name:     "out/",
		Mode:     0755,
		Typeflag: tar.TypeDir,
	}
	if err := tarWriter.WriteHeader(outDirectoryHeader); err != nil {
		return err
	}

	for _, file := range tree.Files() {
		relativeFilePath, err := filepath.Rel(repositoryRoot, file.Path)
		if err != nil {
			return err
		}
		fileInfo, err := os.Lstat(file.Path)
		if err != nil {
			return err
		}

		linkTarget := ""
		if fileInfo.Mode()&os.ModeSymlink != 0 {
			linkTarget, err = os.Readlink(file.Path)
			if err != nil {
				return err
			}
		}

		header, err := tar.FileInfoHeader(fileInfo, linkTarget)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(filepath.Join("src", relativeFilePath))
		if fileInfo.IsDir() {
			header.Name += "/"
		}

		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}

		if fileInfo.Mode().IsRegular() {
			fileData, err := os.Open(file.Path)
			if err != nil {
				return err
			}
			_, copyError := io.Copy(tarWriter, fileData)
			closeError := fileData.Close()
			if copyError != nil {
				return copyError
			}
			if closeError != nil {
				return closeError
			}
		}
	}

	return nil
}

func placeStagedOutputs(targetDirectory, stagingDirectory string, outputs []string, tree *sourcetree.SourceTree) (returnError error) {
	untrustedRoot, err := fileutils.OpenUntrustedRoot(targetDirectory)
	if err != nil {
		return fmt.Errorf("opening root %s: %w", targetDirectory, err)
	}
	defer func() {
		if closeErr := untrustedRoot.Close(); closeErr != nil && returnError == nil {
			returnError = closeErr
		}
	}()

	if isCoLocated(outputs) {
		walkErr := filepath.Walk(stagingDirectory, func(path string, fileInfo os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if fileInfo.IsDir() {
				return nil
			}
			relativePath, err := filepath.Rel(stagingDirectory, path)
			if err != nil {
				return err
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return untrustedRoot.WriteFile(relativePath, content, fileInfo.Mode().Perm())
		})
		if walkErr != nil {
			return fmt.Errorf("placing co-located plugin output: %w", walkErr)
		}
	} else {
		for _, outputEntry := range outputs {
			cleanEntry := filepath.Clean(outputEntry)
			if cleanEntry == ".." || strings.HasPrefix(cleanEntry, ".."+string(filepath.Separator)) || filepath.IsAbs(outputEntry) {
				return fmt.Errorf("output path %q escapes directory %s", outputEntry, targetDirectory)
			}

			stagedPath := filepath.Join(stagingDirectory, outputEntry)
			fileInfo, err := os.Stat(stagedPath)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return fmt.Errorf("stating staged output %s: %w", outputEntry, err)
			}

			if fileInfo.IsDir() {
				walkErr := filepath.Walk(stagedPath, func(path string, childInfo os.FileInfo, err error) error {
					if err != nil {
						return err
					}
					if childInfo.IsDir() {
						return nil
					}
					relativePath, err := filepath.Rel(stagingDirectory, path)
					if err != nil {
						return err
					}
					content, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					return untrustedRoot.WriteFile(relativePath, content, childInfo.Mode().Perm())
				})
				if walkErr != nil {
					return fmt.Errorf("placing plugin output %s: %w", outputEntry, walkErr)
				}
			} else {
				content, err := os.ReadFile(stagedPath)
				if err != nil {
					return err
				}
				if err := untrustedRoot.WriteFile(outputEntry, content, fileInfo.Mode().Perm()); err != nil {
					return fmt.Errorf("placing plugin output %s: %w", outputEntry, err)
				}
			}
		}
	}

	if invalidateErr := tree.InvalidateAll(); invalidateErr != nil {
		return fmt.Errorf("invalidating source tree: %w", invalidateErr)
	}
	return nil
}
