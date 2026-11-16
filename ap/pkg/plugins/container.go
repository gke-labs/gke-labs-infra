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
)

type verifyKey struct{}

// WithVerify returns a context indicating whether verify mode is active.
func WithVerify(ctx context.Context, verify bool) context.Context {
	return context.WithValue(ctx, verifyKey{}, verify)
}

// IsVerify reports whether verify mode is active in the context.
func IsVerify(ctx context.Context) bool {
	v, ok := ctx.Value(verifyKey{}).(bool)
	return ok && v
}

// findContainerEngine detects an available container runtime (docker or podman).
func findContainerEngine(ctx context.Context, scope *tasks.TaskContext) (string, error) {
	if env := os.Getenv("CONTAINER_ENGINE"); env != "" {
		bin, err := exec.LookPath(env)
		if err != nil {
			return "", fmt.Errorf("container engine %q not found in PATH: %w", env, err)
		}
		return bin, nil
	}

	if scope != nil {
		bin, err := scope.Tool(ctx, "docker")
		if err != nil {
			scope.Log().V(2).Info("failed to resolve tool docker", "error", err)
		} else if bin != "" {
			lBin, err := exec.LookPath(bin)
			if err != nil {
				scope.Log().V(2).Info("docker tool not in PATH", "path", bin, "error", err)
			} else {
				return lBin, nil
			}
		}
	}

	bin, err := exec.LookPath("docker")
	if err != nil {
		if scope != nil {
			scope.Log().V(2).Info("docker not in PATH", "error", err)
		}
	} else {
		return bin, nil
	}

	bin, err = exec.LookPath("podman")
	if err != nil {
		if scope != nil {
			scope.Log().V(2).Info("podman not in PATH", "error", err)
		}
	} else {
		return bin, nil
	}

	return "", errors.New("no container runtime (docker or podman) found in PATH")
}

// DescribeContainer runs `<image> describe` in a container on cold runs and caches the manifest.
func DescribeContainer(ctx context.Context, repoRoot, cacheDirectory, pluginName string, declaration config.PluginDeclaration) (*PluginManifest, error) {
	if cacheDirectory == "" {
		c, err := fileutils.CacheDir()
		if err != nil {
			return nil, err
		}
		cacheDirectory = c
	}

	key := CacheKey(declaration)
	manifest, err := LoadCachedManifest(cacheDirectory, key)
	if err == nil {
		return manifest, nil
	}

	engine, err := findContainerEngine(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("plugin %q requires a container runtime: %w", pluginName, err)
	}

	cmd := exec.CommandContext(ctx, engine, "run", "--rm", "--network", "none", declaration.Image, "describe")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("running describe for plugin %q: %w (%s)", pluginName, err, strings.TrimSpace(string(out)))
	}

	var m PluginManifest
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, fmt.Errorf("parsing plugin %q manifest: %w", pluginName, err)
	}

	sanitizedKey := strings.ReplaceAll(strings.ReplaceAll(key, ":", "-"), "/", "-")
	describeDir := filepath.Join(cacheDirectory, "plugins", "describe")
	if err := os.MkdirAll(describeDir, 0755); err != nil {
		return nil, fmt.Errorf("creating describe cache dir %s: %w", describeDir, err)
	}
	manifestPath := filepath.Join(describeDir, sanitizedKey+".json")
	if err := os.WriteFile(manifestPath, out, 0644); err != nil {
		return nil, fmt.Errorf("writing cached manifest %s: %w", manifestPath, err)
	}

	return &m, nil
}

// ContainerPlugin implements Plugin for container-based plugins.
type ContainerPlugin struct {
	Name        string
	Declaration config.PluginDeclaration
	Manifest    *PluginManifest
	RepoRoot    string
}

// Describe returns the manifest describing the container plugin.
func (p *ContainerPlugin) Describe(ctx context.Context) (*PluginManifest, error) {
	return p.Manifest, nil
}

// DiscoverTasks discovers tasks for the container plugin under dir.
func (p *ContainerPlugin) DiscoverTasks(ctx context.Context, dir string, scope *tasks.TaskContext) ([]PluginTask, error) {
	if p.Manifest == nil {
		return nil, nil
	}

	activatedDirs, err := FindActivatedDirs(scope.SourceTree().Under(dir), p.Name, p.Manifest.Activation)
	if err != nil {
		return nil, err
	}

	var discoveredTasks []PluginTask
	for _, actDir := range activatedDirs {
		for _, taskMeta := range p.Manifest.Tasks {
			task := &ContainerTask{
				PluginName:  p.Name,
				TaskMeta:    taskMeta,
				Declaration: p.Declaration,
				Manifest:    p.Manifest,
				Dir:         actDir,
				RepoRoot:    p.RepoRoot,
			}
			discoveredTasks = append(discoveredTasks, PluginTask{
				Plugin:       p.Name,
				TaskMetadata: taskMeta,
				Task:         task,
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
		decl := pluginsConfig.Plugins[name]
		if !decl.IsEnabled() {
			continue
		}
		manifest, err := DescribeContainer(ctx, repoRoot, cacheDirectory, name, decl)
		if err != nil {
			return nil, err
		}
		result = append(result, &ContainerPlugin{
			Name:        name,
			Declaration: decl,
			Manifest:    manifest,
			RepoRoot:    repoRoot,
		})
	}
	return result, nil
}

// ContainerTask represents a task executed inside a container plugin.
type ContainerTask struct {
	PluginName  string
	TaskMeta    TaskMetadata
	Declaration config.PluginDeclaration
	Manifest    *PluginManifest
	Dir         string
	RepoRoot    string
	Verify      bool
}

var (
	_ tasks.Task    = (*ContainerTask)(nil)
	_ tasks.Sourced = (*ContainerTask)(nil)
)

// GetName returns the hyphenated task name `<plugin>-<task>`.
func (t *ContainerTask) GetName() string {
	return fmt.Sprintf("%s-%s", t.PluginName, t.TaskMeta.Name)
}

// GetChildren returns no children for leaf container tasks.
func (t *ContainerTask) GetChildren() []tasks.Task {
	return nil
}

// SourcePath returns the activated directory path for task filtering.
func (t *ContainerTask) SourcePath() string {
	return t.Dir
}

// Run executes the task in a container sandbox.
func (t *ContainerTask) Run(ctx context.Context, scope *tasks.TaskContext) error {
	// Network authorization check: manifest requires network, repository must authorize
	if t.Manifest != nil && t.Manifest.Network && !t.Declaration.Network {
		return fmt.Errorf("plugin %q requires network access, but network is not authorized in .ap/plugins.yaml", t.PluginName)
	}

	engine, err := findContainerEngine(ctx, scope)
	if err != nil {
		return fmt.Errorf("plugin %q in %s requires a container runtime: %w", t.PluginName, t.Dir, err)
	}

	var createArgs []string
	createArgs = append(createArgs, "create")
	if t.Manifest == nil || !t.Manifest.Network || !t.Declaration.Network {
		createArgs = append(createArgs, "--network", "none")
	}
	createArgs = append(createArgs, t.Declaration.Image, "run", t.TaskMeta.Name, "--src", "/src", "--out", "/out")

	createCmd := exec.CommandContext(ctx, engine, createArgs...)
	createOut, err := createCmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("creating container for plugin %q: %w (%s)", t.PluginName, err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return fmt.Errorf("creating container for plugin %q: %w", t.PluginName, err)
	}
	containerID := strings.TrimSpace(string(createOut))

	// Ensure container is always removed regardless of outcome or cancellation
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(cleanupCtx, engine, "rm", "-f", containerID).CombinedOutput(); err != nil {
			scope.Log().V(2).Info("failed to remove plugin container", "container", containerID, "error", err, "output", strings.TrimSpace(string(out)))
		}
	}()

	// Stream input source tree into container /src
	pr, pw := io.Pipe()
	cpInCmd := exec.CommandContext(ctx, engine, "cp", "-", containerID+":/")
	cpInCmd.Stdin = pr

	tarErrCh := make(chan error, 1)
	go func() {
		tarErr := writeTarStream(scope.SourceTree().Under(t.Dir), t.Dir, pw)
		_ = pw.CloseWithError(tarErr)
		tarErrCh <- tarErr
	}()

	cpInOutput, cpInErr := cpInCmd.CombinedOutput()
	_ = pr.CloseWithError(cpInErr)

	tarErr := <-tarErrCh
	if cpInErr != nil {
		return fmt.Errorf("copying source into plugin container %s: %w (%s)", containerID, cpInErr, strings.TrimSpace(string(cpInOutput)))
	}
	if tarErr != nil && !errors.Is(tarErr, io.ErrClosedPipe) {
		return fmt.Errorf("creating source archive for plugin %q: %w", t.PluginName, tarErr)
	}

	// Run task inside container
	startCmd := exec.CommandContext(ctx, engine, "start", "-a", containerID)
	startCmd.Stdout = scope.Stdout()
	startCmd.Stderr = scope.Stderr()
	if err := startCmd.Run(); err != nil {
		return fmt.Errorf("plugin %q task %q failed: %w", t.PluginName, t.TaskMeta.Name, err)
	}

	if len(t.TaskMeta.Outputs) == 0 {
		return nil
	}

	// Copy outputs from container /out/. to host staging directory
	stagingDir, err := os.MkdirTemp("", "ap-plugin-stage-*")
	if err != nil {
		return fmt.Errorf("failed to create staging directory: %w", err)
	}
	defer func() {
		if rmErr := os.RemoveAll(stagingDir); rmErr != nil && !os.IsNotExist(rmErr) {
			scope.Log().V(2).Info("failed to remove staging directory", "dir", stagingDir, "error", rmErr)
		}
	}()

	cpOutCmd := exec.CommandContext(ctx, engine, "cp", containerID+":/out/.", stagingDir)
	if cpOut, err := cpOutCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("copying output from container %s: %w (%s)", containerID, err, strings.TrimSpace(string(cpOut)))
	}

	isVerify := t.Verify || IsVerify(ctx)
	if isVerify {
		return verifyStagedOutputs(t.Dir, stagingDir, t.TaskMeta.Outputs, scope.Stderr())
	}

	return placeStagedOutputs(t.Dir, stagingDir, t.TaskMeta.Outputs, scope.SourceTree())
}

func isCoLocated(outputs []string) bool {
	return len(outputs) == 1 && outputs[0] == "."
}

// writeTarStream creates a tar stream containing files from tree under "src/", and ensures "out/" exists.
func writeTarStream(tree *sourcetree.SourceTree, baseDir string, w io.Writer) (retErr error) {
	tw := tar.NewWriter(w)
	defer func() {
		if err := tw.Close(); err != nil && retErr == nil {
			retErr = err
		}
	}()

	// Ensure out/ exists inside container
	outHeader := &tar.Header{
		Name:     "out/",
		Mode:     0755,
		Typeflag: tar.TypeDir,
	}
	if err := tw.WriteHeader(outHeader); err != nil {
		return err
	}

	if tree == nil {
		return nil
	}

	for _, f := range tree.Files() {
		rel, err := filepath.Rel(baseDir, f.Path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(f.Path)
		if err != nil {
			return err
		}

		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(f.Path)
			if err != nil {
				return err
			}
		}

		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(filepath.Join("src", rel))
		if info.IsDir() {
			header.Name += "/"
		}

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if info.Mode().IsRegular() {
			fileData, err := os.Open(f.Path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tw, fileData)
			closeErr := fileData.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}

	return nil
}

// placeStagedOutputs synchronizes staged outputs into targetDir using UntrustedRoot.
func placeStagedOutputs(targetDir, stagingDir string, outputs []string, tree *sourcetree.SourceTree) (retErr error) {
	untrustedRoot, err := fileutils.OpenUntrustedRoot(targetDir)
	if err != nil {
		return fmt.Errorf("failed to open root %s: %w", targetDir, err)
	}
	defer func() {
		if closeErr := untrustedRoot.Close(); closeErr != nil && retErr == nil {
			retErr = closeErr
		}
	}()

	if isCoLocated(outputs) {
		err := filepath.Walk(stagingDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(stagingDir, path)
			if err != nil {
				return err
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return untrustedRoot.WriteFile(rel, content, info.Mode().Perm())
		})
		if err != nil {
			return fmt.Errorf("failed to place co-located plugin output: %w", err)
		}
	} else {
		for _, entry := range outputs {
			stagedPath := filepath.Join(stagingDir, entry)
			info, err := os.Stat(stagedPath)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return fmt.Errorf("failed to stat staged output %s: %w", entry, err)
			}

			if info.IsDir() {
				err := filepath.Walk(stagedPath, func(path string, fi os.FileInfo, err error) error {
					if err != nil {
						return err
					}
					if fi.IsDir() {
						return nil
					}
					rel, err := filepath.Rel(stagingDir, path)
					if err != nil {
						return err
					}
					content, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					return untrustedRoot.WriteFile(rel, content, fi.Mode().Perm())
				})
				if err != nil {
					return fmt.Errorf("failed to place plugin output %s: %w", entry, err)
				}
			} else {
				content, err := os.ReadFile(stagedPath)
				if err != nil {
					return err
				}
				if err := untrustedRoot.WriteFile(entry, content, info.Mode().Perm()); err != nil {
					return fmt.Errorf("failed to place plugin output %s: %w", entry, err)
				}
			}
		}
	}

	if tree != nil {
		if invErr := tree.InvalidateAll(); invErr != nil {
			return fmt.Errorf("invalidating source tree: %w", invErr)
		}
	}
	return nil
}

// verifyStagedOutputs compares staged outputs against the repository without modifying files.
func verifyStagedOutputs(targetDir, stagingDir string, outputs []string, errOut io.Writer) error {
	var diffs []string

	if isCoLocated(outputs) {
		err := filepath.Walk(stagingDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(stagingDir, path)
			if err != nil {
				return err
			}
			stagedContent, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			repoPath := filepath.Join(targetDir, rel)
			repoContent, err := os.ReadFile(repoPath)
			if err != nil {
				if os.IsNotExist(err) {
					diffs = append(diffs, fmt.Sprintf("file %s is missing from repository", rel))
					return nil
				}
				return err
			}

			if !bytes.Equal(stagedContent, repoContent) {
				diffs = append(diffs, fmt.Sprintf("file %s differs from repository state", rel))
			}
			return nil
		})
		if err != nil {
			return err
		}
	} else {
		for _, entry := range outputs {
			stagedPath := filepath.Join(stagingDir, entry)
			repoPath := filepath.Join(targetDir, entry)

			stagedInfo, err := os.Stat(stagedPath)
			if err != nil {
				if os.IsNotExist(err) {
					diffs = append(diffs, fmt.Sprintf("expected output %s was not produced", entry))
					continue
				}
				return err
			}

			if stagedInfo.IsDir() {
				stagedFiles := make(map[string][]byte)
				err := filepath.Walk(stagedPath, func(path string, info os.FileInfo, err error) error {
					if err != nil || info.IsDir() {
						return err
					}
					rel, rErr := filepath.Rel(stagedPath, path)
					if rErr != nil {
						return rErr
					}
					content, rErr := os.ReadFile(path)
					if rErr != nil {
						return rErr
					}
					stagedFiles[rel] = content
					return nil
				})
				if err != nil {
					return err
				}

				repoFiles := make(map[string][]byte)
				if repoInfo, rErr := os.Stat(repoPath); rErr == nil && repoInfo.IsDir() {
					wErr := filepath.Walk(repoPath, func(path string, info os.FileInfo, err error) error {
						if err != nil || info.IsDir() {
							return err
						}
						rel, rErr := filepath.Rel(repoPath, path)
						if rErr != nil {
							return rErr
						}
						content, rErr := os.ReadFile(path)
						if rErr != nil {
							return rErr
						}
						repoFiles[rel] = content
						return nil
					})
					if wErr != nil {
						return wErr
					}
				} else {
					diffs = append(diffs, fmt.Sprintf("directory %s is missing from repository", entry))
					continue
				}

				for rel, sBytes := range stagedFiles {
					rBytes, ok := repoFiles[rel]
					if !ok {
						diffs = append(diffs, fmt.Sprintf("file %s is missing from repository in %s", rel, entry))
					} else if !bytes.Equal(sBytes, rBytes) {
						diffs = append(diffs, fmt.Sprintf("file %s in %s differs from repository state", rel, entry))
					}
				}
				for rel := range repoFiles {
					if _, ok := stagedFiles[rel]; !ok {
						diffs = append(diffs, fmt.Sprintf("unexpected file %s present in repository %s", rel, entry))
					}
				}
			} else {
				stagedContent, err := os.ReadFile(stagedPath)
				if err != nil {
					return err
				}
				repoContent, err := os.ReadFile(repoPath)
				if err != nil {
					if os.IsNotExist(err) {
						diffs = append(diffs, fmt.Sprintf("file %s is missing from repository", entry))
						continue
					}
					return err
				}
				if !bytes.Equal(stagedContent, repoContent) {
					diffs = append(diffs, fmt.Sprintf("file %s differs from repository state", entry))
				}
			}
		}
	}

	if len(diffs) > 0 {
		sort.Strings(diffs)
		var sb strings.Builder
		sb.WriteString("verify failed for plugin outputs:\n")
		for _, d := range diffs {
			sb.WriteString("  - ")
			sb.WriteString(d)
			sb.WriteString("\n")
		}
		if errOut != nil {
			if _, pErr := fmt.Fprint(errOut, sb.String()); pErr != nil {
				return errors.Join(errors.New(strings.TrimSpace(sb.String())), pErr)
			}
		}
		return errors.New(strings.TrimSpace(sb.String()))
	}
	return nil
}
