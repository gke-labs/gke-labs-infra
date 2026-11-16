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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/images"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

func buildPluginImage(ctx context.Context, scope *tasks.TaskContext, pluginName, dockerfilePath, buildkitHost string) (string, error) {
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

// DescribeContainer runs `<image> describe` in a container on cold runs and caches the manifest.
func DescribeContainer(ctx context.Context, scope *tasks.TaskContext, cacheDirectory, pluginName, imageRef string) (*PluginManifest, error) {
	key := CacheKey(imageRef)
	manifest, err := LoadCachedManifest(cacheDirectory, key)
	if err == nil {
		return manifest, nil
	}

	dockerPath, err := scope.Tool(ctx, "docker")
	if err != nil {
		return nil, fmt.Errorf("plugin %q requires a container runtime (docker): %w", pluginName, err)
	}

	command := exec.CommandContext(ctx, dockerPath, "run", "--rm", "--network", "none", imageRef, "describe")
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

	return &parsedManifest, nil
}

// ContainerPlugin implements Plugin for container-based plugins.
type ContainerPlugin struct {
	Name        string
	Declaration config.PluginDeclaration
	ImageRef    string
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

// LoadContainerPlugins loads all enabled container plugins configured in .ap/plugins.yaml.
func LoadContainerPlugins(ctx context.Context, scope *tasks.TaskContext, cacheDirectory, buildkitHost string) ([]Plugin, error) {
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
			var err error
			imageRef, err = buildPluginImage(ctx, scope, name, declaration.Dockerfile, buildkitHost)
			if err != nil {
				return nil, err
			}
		}
		manifest, err := DescribeContainer(ctx, scope, cacheDirectory, name, imageRef)
		if err != nil {
			return nil, err
		}
		result = append(result, &ContainerPlugin{
			Name:        name,
			Declaration: declaration,
			ImageRef:    imageRef,
			Manifest:    manifest,
			RepoRoot:    scope.RepoRoot(),
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
	Verify            bool
}

// SetVerify enables or disables verify mode on the container task.
func (task *ContainerTask) SetVerify(verify bool) {
	task.Verify = verify
}

var (
	_ tasks.Task    = (*ContainerTask)(nil)
	_ tasks.Sourced = (*ContainerTask)(nil)
)

// GetName returns the hyphenated task name `<plugin>-<task>`, suffixed by the activating directory.
func (task *ContainerTask) GetName() string {
	name := fmt.Sprintf("%s-%s", task.PluginName, task.TaskMetadata.Name)
	if task.RelativeDirectory == "" || task.RelativeDirectory == "." {
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

// Outputs returns the task's configured output paths.
func (task *ContainerTask) Outputs() []string {
	return task.TaskMetadata.Outputs
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

	relativeDirectory := task.RelativeDirectory
	if relativeDirectory == "" {
		relativeDirectory = "."
	}

	var createArguments []string
	createArguments = append(createArguments, "create")
	if !task.Manifest.Network || !task.Declaration.Network {
		createArguments = append(createArguments, "--network", "none")
	}
	createArguments = append(createArguments, task.ImageRef, "run", task.TaskMetadata.Name, "--src", "/src", "--dir", relativeDirectory, "--out", "/out")

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
			scoped.Log(ctx).V(2).Info("failed to remove plugin container", "container", containerID, "error", removeErr, "output", strings.TrimSpace(string(output)))
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
			scoped.Log(ctx).V(2).Info("failed to remove staging directory", "dir", stagingDirectory, "error", removeErr)
		}
	}()

	copyOutCommand := exec.CommandContext(ctx, dockerPath, "cp", containerID+":/out/.", stagingDirectory)
	if copyOutOutput, err := copyOutCommand.CombinedOutput(); err != nil {
		return fmt.Errorf("copying output from container %s: %w (%s)", containerID, err, strings.TrimSpace(string(copyOutOutput)))
	}

	if task.Verify {
		return verifyStagedOutputs(task.Dir, stagingDirectory, task.TaskMetadata.Outputs, scope.Stderr())
	}

	return placeStagedOutputs(task.Dir, stagingDirectory, task.TaskMetadata.Outputs, scope.SourceTree())
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

	if err := untrustedRoot.CopyStagedOutputs(stagingDirectory, outputs); err != nil {
		return fmt.Errorf("placing plugin outputs into %s: %w", targetDirectory, err)
	}

	if invalidateErr := tree.InvalidateAll(); invalidateErr != nil {
		return fmt.Errorf("invalidating source tree: %w", invalidateErr)
	}
	return nil
}

func isCoLocated(outputs []string) bool {
	return len(outputs) == 1 && (outputs[0] == "." || filepath.Clean(outputs[0]) == ".")
}

func verifyStagedOutputs(targetDirectory, stagingDirectory string, outputs []string, errOut io.Writer) (returnError error) {
	stagingRoot, err := os.OpenRoot(stagingDirectory)
	if err != nil {
		return fmt.Errorf("opening staging root %s: %w", stagingDirectory, err)
	}
	defer func() {
		if closeErr := stagingRoot.Close(); closeErr != nil && returnError == nil {
			returnError = closeErr
		}
	}()

	targetRoot, err := os.OpenRoot(targetDirectory)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("opening target root %s: %w", targetDirectory, err)
	}
	if targetRoot != nil {
		defer func() {
			if closeErr := targetRoot.Close(); closeErr != nil && returnError == nil {
				returnError = closeErr
			}
		}()
	}

	var diffs []string

	if isCoLocated(outputs) {
		walkErr := fs.WalkDir(stagingRoot.FS(), ".", func(path string, d fs.DirEntry, walkError error) error {
			if walkError != nil {
				return walkError
			}
			if path == "." {
				return nil
			}
			info, err := stagingRoot.Lstat(path)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%s: is a symlink", path)
			}
			if info.IsDir() {
				return nil
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s: is not a regular file", path)
			}
			stagedContent, err := stagingRoot.ReadFile(path)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}

			var repoContent []byte
			var repoExists bool
			if targetRoot != nil {
				content, err := targetRoot.ReadFile(path)
				if err == nil {
					repoContent = content
					repoExists = true
				} else if !os.IsNotExist(err) {
					return fmt.Errorf("%s: %w", path, err)
				}
			}

			cleanSlash := filepath.ToSlash(path)
			if !repoExists {
				diffs = append(diffs, unifiedDiff("/dev/null", "b/"+cleanSlash, nil, splitLines(string(stagedContent))))
			} else if !bytes.Equal(stagedContent, repoContent) {
				diffs = append(diffs, unifiedDiff("a/"+cleanSlash, "b/"+cleanSlash, splitLines(string(repoContent)), splitLines(string(stagedContent))))
			}
			return nil
		})
		if walkErr != nil {
			return walkErr
		}
	} else {
		for _, outputEntry := range outputs {
			cleanEntry := filepath.Clean(outputEntry)
			if cleanEntry == ".." || strings.HasPrefix(cleanEntry, ".."+string(filepath.Separator)) || filepath.IsAbs(outputEntry) {
				return fmt.Errorf("%s: escapes staging root", outputEntry)
			}
			if hasGitComponent(cleanEntry) {
				return fmt.Errorf("%s: is under .git", outputEntry)
			}

			entryInfo, err := stagingRoot.Lstat(cleanEntry)
			if err != nil {
				if os.IsNotExist(err) {
					diffs = append(diffs, fmt.Sprintf("expected output %s was not produced", cleanEntry))
					continue
				}
				return fmt.Errorf("%s: %w", cleanEntry, err)
			}
			if entryInfo.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%s: is a symlink", cleanEntry)
			}
			if !entryInfo.Mode().IsRegular() && !entryInfo.IsDir() {
				return fmt.Errorf("%s: is not a regular file or directory", cleanEntry)
			}

			if entryInfo.IsDir() {
				stagedFiles := make(map[string][]byte)
				walkErr := fs.WalkDir(stagingRoot.FS(), cleanEntry, func(path string, d fs.DirEntry, walkError error) error {
					if walkError != nil {
						return walkError
					}
					if path == "." {
						return nil
					}
					info, err := stagingRoot.Lstat(path)
					if err != nil {
						return fmt.Errorf("%s: %w", path, err)
					}
					if info.Mode()&os.ModeSymlink != 0 {
						return fmt.Errorf("%s: is a symlink", path)
					}
					if info.IsDir() {
						return nil
					}
					if !info.Mode().IsRegular() {
						return fmt.Errorf("%s: is not a regular file", path)
					}
					content, err := stagingRoot.ReadFile(path)
					if err != nil {
						return fmt.Errorf("%s: %w", path, err)
					}
					stagedFiles[filepath.ToSlash(path)] = content
					return nil
				})
				if walkErr != nil {
					return walkErr
				}

				repoFiles := make(map[string][]byte)
				if targetRoot != nil {
					if tInfo, err := targetRoot.Lstat(cleanEntry); err == nil && tInfo.IsDir() {
						repoWalkErr := fs.WalkDir(targetRoot.FS(), cleanEntry, func(path string, d fs.DirEntry, walkError error) error {
							if walkError != nil {
								return walkError
							}
							if d.IsDir() {
								return nil
							}
							content, err := targetRoot.ReadFile(path)
							if err != nil {
								return fmt.Errorf("%s: %w", path, err)
							}
							repoFiles[filepath.ToSlash(path)] = content
							return nil
						})
						if repoWalkErr != nil {
							return repoWalkErr
						}
					}
				}

				allPathsMap := make(map[string]struct{})
				for p := range stagedFiles {
					allPathsMap[p] = struct{}{}
				}
				for p := range repoFiles {
					allPathsMap[p] = struct{}{}
				}
				var allPaths []string
				for p := range allPathsMap {
					allPaths = append(allPaths, p)
				}
				sort.Strings(allPaths)

				for _, p := range allPaths {
					sBytes, inStaged := stagedFiles[p]
					rBytes, inRepo := repoFiles[p]
					if inStaged && !inRepo {
						diffs = append(diffs, unifiedDiff("/dev/null", "b/"+p, nil, splitLines(string(sBytes))))
					} else if !inStaged && inRepo {
						diffs = append(diffs, unifiedDiff("a/"+p, "/dev/null", splitLines(string(rBytes)), nil))
					} else if !bytes.Equal(sBytes, rBytes) {
						diffs = append(diffs, unifiedDiff("a/"+p, "b/"+p, splitLines(string(rBytes)), splitLines(string(sBytes))))
					}
				}
			} else {
				stagedContent, err := stagingRoot.ReadFile(cleanEntry)
				if err != nil {
					return fmt.Errorf("%s: %w", cleanEntry, err)
				}
				var repoContent []byte
				var repoExists bool
				if targetRoot != nil {
					content, err := targetRoot.ReadFile(cleanEntry)
					if err == nil {
						repoContent = content
						repoExists = true
					} else if !os.IsNotExist(err) {
						return fmt.Errorf("%s: %w", cleanEntry, err)
					}
				}
				cleanSlash := filepath.ToSlash(cleanEntry)
				if !repoExists {
					diffs = append(diffs, unifiedDiff("/dev/null", "b/"+cleanSlash, nil, splitLines(string(stagedContent))))
				} else if !bytes.Equal(stagedContent, repoContent) {
					diffs = append(diffs, unifiedDiff("a/"+cleanSlash, "b/"+cleanSlash, splitLines(string(repoContent)), splitLines(string(stagedContent))))
				}
			}
		}
	}

	if len(diffs) > 0 {
		diffText := strings.Join(diffs, "\n")
		if errOut != nil {
			if _, printErr := fmt.Fprintln(errOut, diffText); printErr != nil {
				return fmt.Errorf("writing diff: %w", printErr)
			}
		}
		return fmt.Errorf("plugin outputs differ from checkout:\n%s", diffText)
	}

	return nil
}

func hasGitComponent(path string) bool {
	for _, component := range strings.Split(filepath.ToSlash(filepath.Clean(path)), "/") {
		if strings.EqualFold(component, ".git") {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func unifiedDiff(oldName, newName string, oldLines, newLines []string) string {
	n, m := len(oldLines), len(newLines)

	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := 0; i < n; i++ {
		for j := 0; j < m; j++ {
			if oldLines[i] == newLines[j] {
				dp[i+1][j+1] = dp[i][j] + 1
			} else if dp[i+1][j] > dp[i][j+1] {
				dp[i+1][j+1] = dp[i+1][j]
			} else {
				dp[i+1][j+1] = dp[i][j+1]
			}
		}
	}

	var diff []string
	i, j := n, m
	for i > 0 || j > 0 {
		if i > 0 && j > 0 && oldLines[i-1] == newLines[j-1] {
			diff = append(diff, " "+oldLines[i-1])
			i--
			j--
		} else if j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]) {
			diff = append(diff, "+"+newLines[j-1])
			j--
		} else if i > 0 && (j == 0 || dp[i][j-1] < dp[i-1][j]) {
			diff = append(diff, "-"+oldLines[i-1])
			i--
		}
	}
	slices.Reverse(diff)

	oldHunk := fmt.Sprintf("-1,%d", n)
	if n == 0 {
		oldHunk = "-0,0"
	}
	newHunk := fmt.Sprintf("+1,%d", m)
	if m == 0 {
		newHunk = "+0,0"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("--- %s\n+++ %s\n@@ %s %s @@\n", oldName, newName, oldHunk, newHunk))
	for _, l := range diff {
		sb.WriteString(l)
		sb.WriteString("\n")
	}
	return sb.String()
}
