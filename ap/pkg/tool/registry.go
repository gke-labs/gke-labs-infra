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

package tool

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/fileutils"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/docker"
	gotool "github.com/gke-labs/gke-labs-infra/ap/pkg/tool/go"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/kind"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/protoc"
)

// Invocation represents the prepared execution parameters for a tool.
type Invocation struct {
	Binary string
	Args   []string
	Env    []string
	Dir    string
}

// RunOptions specifies options when running a tool via Run.
type RunOptions struct {
	RepoRoot string
	Args     []string
	DryRun   bool
	Stdout   io.Writer
	Stderr   io.Writer
	Stdin    io.Reader
}

// Tool represents a managed external tool.
type Tool struct {
	Name       string
	Short      string
	UpdateFlag string
	UpdateDesc string

	// Ensure downloads/caches the tool binary if needed and returns the binary path.
	Ensure func(ctx context.Context, repoRoot string) (string, error)

	// Prepare builds the Invocation for running the tool with the given args.
	Prepare func(ctx context.Context, repoRoot string, args []string) (*Invocation, error)

	// Update updates baked-in release versions and checksums in versions.yaml.
	Update func(ctx context.Context, repoRoot string) error
}

// Run executes the tool as a child process using the provided options.
func (t *Tool) Run(ctx context.Context, opt RunOptions) error {
	inv, err := t.Prepare(ctx, opt.RepoRoot, opt.Args)
	if err != nil {
		return err
	}
	if opt.DryRun {
		stdout := opt.Stdout
		if stdout == nil {
			stdout = os.Stdout
		}
		_, err := fmt.Fprintf(stdout, "[dry-run] %s %s\n", inv.Binary, strings.Join(inv.Args, " "))
		return err
	}
	cmd := exec.CommandContext(ctx, inv.Binary, inv.Args...)
	cmd.Env = inv.Env
	cmd.Dir = inv.Dir
	cmd.Stdout = opt.Stdout
	cmd.Stderr = opt.Stderr
	cmd.Stdin = opt.Stdin
	return cmd.Run()
}

// Exec executes the tool pass-through using syscall.Exec on Unix or mirroring as child process on Windows.
func (t *Tool) Exec(ctx context.Context, repoRoot string, args []string) error {
	inv, err := t.Prepare(ctx, repoRoot, args)
	if err != nil {
		return err
	}
	return execProcess(inv)
}

func loadDockerConfig(repoRoot string) (*docker.Config, error) {
	target := repoRoot
	if target == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		target = wd
	}
	cfg, err := docker.LoadConfig(target)
	if err != nil {
		return nil, err
	}
	if cfg != nil {
		return cfg, nil
	}
	return docker.DefaultConfig(), nil
}

func loadKindConfig(repoRoot string) (*kind.Config, error) {
	target := repoRoot
	if target == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		target = wd
	}
	cfg, err := kind.LoadConfig(target)
	if err != nil {
		return nil, err
	}
	if cfg != nil {
		return cfg, nil
	}
	return kind.DefaultConfig(), nil
}

func loadProtocConfig(repoRoot string) (*protoc.Config, error) {
	target := repoRoot
	if target == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		target = wd
	}
	cfg, err := protoc.LoadConfig(target)
	if err != nil {
		return nil, err
	}
	if cfg != nil {
		return cfg, nil
	}
	return protoc.DefaultConfig(), nil
}

var registry = []*Tool{
	{
		Name:       "docker",
		Short:      "Run docker with pinned binary and buildx plugin",
		UpdateFlag: "docker",
		UpdateDesc: "Update Docker versions in ap/pkg/tool/docker/versions.yaml",
		Ensure: func(ctx context.Context, repoRoot string) (string, error) {
			cfg, err := loadDockerConfig(repoRoot)
			if err != nil {
				return "", err
			}
			return docker.EnsureDocker(ctx, cfg.Docker)
		},
		Prepare: func(ctx context.Context, repoRoot string, args []string) (*Invocation, error) {
			cfg, err := loadDockerConfig(repoRoot)
			if err != nil {
				return nil, err
			}
			if len(args) > 0 && args[0] == "buildx" {
				buildxBin, err := docker.EnsureDockerBuildx(ctx, cfg.Buildx)
				if err != nil {
					return nil, fmt.Errorf("failed to prepare docker-buildx: %w", err)
				}
				return &Invocation{
					Binary: buildxBin,
					Args:   args[1:],
					Env:    fileutils.PrependPath(os.Environ(), filepath.Dir(buildxBin)),
				}, nil
			}
			dockerBin, err := docker.EnsureDocker(ctx, cfg.Docker)
			if err != nil {
				return nil, fmt.Errorf("failed to prepare docker: %w", err)
			}
			return &Invocation{
				Binary: dockerBin,
				Args:   args,
				Env:    fileutils.PrependPath(os.Environ(), filepath.Dir(dockerBin)),
			}, nil
		},
		Update: func(ctx context.Context, repoRoot string) error {
			return docker.Update(ctx, repoRoot, docker.UpdateOptions{Docker: true, Buildx: false})
		},
	},
	{
		Name:       "docker-buildx",
		Short:      "Run docker-buildx with pinned binary",
		UpdateFlag: "buildx",
		UpdateDesc: "Update Docker Buildx versions in ap/pkg/tool/docker/versions.yaml",
		Ensure: func(ctx context.Context, repoRoot string) (string, error) {
			cfg, err := loadDockerConfig(repoRoot)
			if err != nil {
				return "", err
			}
			return docker.EnsureDockerBuildx(ctx, cfg.Buildx)
		},
		Prepare: func(ctx context.Context, repoRoot string, args []string) (*Invocation, error) {
			cfg, err := loadDockerConfig(repoRoot)
			if err != nil {
				return nil, err
			}
			buildxBin, err := docker.EnsureDockerBuildx(ctx, cfg.Buildx)
			if err != nil {
				return nil, fmt.Errorf("failed to prepare docker-buildx: %w", err)
			}
			return &Invocation{
				Binary: buildxBin,
				Args:   args,
				Env:    fileutils.PrependPath(os.Environ(), filepath.Dir(buildxBin)),
			}, nil
		},
		Update: func(ctx context.Context, repoRoot string) error {
			return docker.Update(ctx, repoRoot, docker.UpdateOptions{Docker: false, Buildx: true})
		},
	},
	{
		Name:       "kind",
		Short:      "Run kind with pinned binary",
		UpdateFlag: "kind",
		UpdateDesc: "Update kind versions in ap/pkg/tool/kind/versions.yaml",
		Ensure: func(ctx context.Context, repoRoot string) (string, error) {
			cfg, err := loadKindConfig(repoRoot)
			if err != nil {
				return "", err
			}
			return kind.EnsureKind(ctx, cfg.Kind)
		},
		Prepare: func(ctx context.Context, repoRoot string, args []string) (*Invocation, error) {
			cfg, err := loadKindConfig(repoRoot)
			if err != nil {
				return nil, err
			}
			kindBin, err := kind.EnsureKind(ctx, cfg.Kind)
			if err != nil {
				return nil, fmt.Errorf("failed to prepare kind: %w", err)
			}
			return &Invocation{
				Binary: kindBin,
				Args:   args,
				Env:    fileutils.PrependPath(os.Environ(), filepath.Dir(kindBin)),
			}, nil
		},
		Update: func(ctx context.Context, repoRoot string) error {
			return kind.Update(ctx, repoRoot)
		},
	},
	{
		Name:       "protoc",
		Short:      "Run protoc with pinned binary and language plugins",
		UpdateFlag: "protoc",
		UpdateDesc: "Update protoc and plugin versions in ap/pkg/tool/protoc/versions.yaml",
		Ensure: func(ctx context.Context, repoRoot string) (string, error) {
			cfg, err := loadProtocConfig(repoRoot)
			if err != nil {
				return "", err
			}
			binPath, _, err := protoc.EnsureProtoc(ctx, cfg.Protoc)
			return binPath, err
		},
		Prepare: func(ctx context.Context, repoRoot string, args []string) (*Invocation, error) {
			cfg, err := loadProtocConfig(repoRoot)
			if err != nil {
				return nil, err
			}
			env, err := protoc.PrepareProtoc(ctx, cfg, args)
			if err != nil {
				return nil, err
			}
			finalArgs := make([]string, 0, len(args)+2)
			finalArgs = append(finalArgs, args...)

			hasUserInclude := false
			hasInclude := false
			for _, arg := range args {
				if strings.HasPrefix(arg, "-I") || strings.HasPrefix(arg, "--proto_path") {
					hasUserInclude = true
				}
				if arg == "-I"+env.IncludeDir || arg == env.IncludeDir {
					hasInclude = true
				}
			}
			if !hasUserInclude {
				finalArgs = append(finalArgs, "-I.")
			}
			if !hasInclude && env.IncludeDir != "" {
				finalArgs = append(finalArgs, "-I"+env.IncludeDir)
			}
			return &Invocation{
				Binary: env.ProtocBin,
				Args:   finalArgs,
				Env:    env.Env,
				Dir:    "",
			}, nil
		},
		Update: func(ctx context.Context, repoRoot string) error {
			return protoc.Update(ctx, repoRoot)
		},
	},
	{
		Name:       "go",
		Short:      "Run go with pinned toolchain",
		UpdateFlag: "go",
		UpdateDesc: "Update Go versions in ap/pkg/tool/go/versions.yaml",
		Ensure: func(ctx context.Context, repoRoot string) (string, error) {
			return gotool.EnsureGoForRepo(ctx, repoRoot)
		},
		Prepare: func(ctx context.Context, repoRoot string, args []string) (*Invocation, error) {
			target := repoRoot
			if target == "" {
				wd, err := os.Getwd()
				if err != nil {
					return nil, err
				}
				target = wd
			}
			version, err := gotool.ResolveVersion(target, repoRoot)
			if err != nil {
				return nil, err
			}
			goBin, err := gotool.EnsureGo(ctx, version)
			if err != nil {
				return nil, fmt.Errorf("failed to prepare go: %w", err)
			}

			installDir := filepath.Dir(filepath.Dir(goBin))

			env := os.Environ()
			var filteredEnv []string
			for _, e := range env {
				if strings.HasPrefix(e, "GOROOT=") || strings.HasPrefix(e, "GOTOOLCHAIN=") {
					continue
				}
				filteredEnv = append(filteredEnv, e)
			}
			filteredEnv = append(filteredEnv, "GOROOT="+installDir, "GOTOOLCHAIN=local")

			return &Invocation{
				Binary: goBin,
				Args:   args,
				Env:    fileutils.PrependPath(filteredEnv, filepath.Dir(goBin)),
				Dir:    "",
			}, nil
		},
		Update: func(ctx context.Context, repoRoot string) error {
			return gotool.Update(ctx, repoRoot)
		},
	},
}

// All returns a slice of all registered tools in the registry.
func All() []*Tool {
	res := make([]*Tool, len(registry))
	copy(res, registry)
	return res
}

// Lookup finds a tool by its exact registered name.
func Lookup(name string) (*Tool, bool) {
	for _, t := range registry {
		if t.Name == name {
			return t, true
		}
	}
	return nil, false
}

// Resolve resolves a tool from an argv[0] path, stripping directories and .exe suffix.
func Resolve(argv0 string) (*Tool, bool) {
	base := filepath.Base(argv0)
	if idx := strings.LastIndex(base, "\\"); idx != -1 {
		base = base[idx+1:]
	}
	base = strings.TrimSuffix(base, ".exe")
	return Lookup(base)
}

// FindRoots attempts to discover the repository root and ap root from environment or current working directory.
func FindRoots() (repoRoot, apRoot string) {
	repoRoot = os.Getenv("REPO_ROOT")
	apRoot = os.Getenv("AP_ROOT")

	if repoRoot != "" && apRoot != "" {
		return repoRoot, apRoot
	}

	startDir, err := os.Getwd()
	if err != nil {
		return repoRoot, apRoot
	}

	dir := startDir
	for {
		if apRoot == "" {
			hasAP, err := fileutils.FileExists(filepath.Join(dir, ".ap"))
			if err != nil {
				return repoRoot, apRoot
			}
			if hasAP {
				apRoot = dir
			}
		}
		hasGit, err := fileutils.FileExists(filepath.Join(dir, ".git"))
		if err != nil {
			return repoRoot, apRoot
		}
		if hasGit {
			if repoRoot == "" {
				repoRoot = dir
			}
			if apRoot == "" {
				apRoot = dir
			}
			return repoRoot, apRoot
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	if repoRoot == "" {
		repoRoot = apRoot
	}
	return repoRoot, apRoot
}

// FindRepoRoot attempts to discover the closest repo/ap root for tool configuration.
func FindRepoRoot() string {
	repoRoot, apRoot := FindRoots()
	if apRoot != "" {
		return apRoot
	}
	return repoRoot
}
