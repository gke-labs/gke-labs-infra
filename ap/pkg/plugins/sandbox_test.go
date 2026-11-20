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

package plugins_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/clix/sandbox"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins/api"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins/stdio"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"google.golang.org/grpc"
)

type mockSandboxBackend struct {
	runFunction func(ctx context.Context, config sandbox.Config, args []string, options sandbox.RunOptions) error
}

func (backend *mockSandboxBackend) Run(ctx context.Context, config sandbox.Config, args []string, options sandbox.RunOptions) error {
	if backend.runFunction != nil {
		return backend.runFunction(ctx, config, args, options)
	}
	return nil
}

func (backend *mockSandboxBackend) Usable(ctx context.Context) error { return nil }
func (backend *mockSandboxBackend) String() string                   { return "mock" }

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

type testPluginServer struct {
	api.UnimplementedPluginServiceServer
}

func (server *testPluginServer) Describe(ctx context.Context, req *api.DescribeRequest) (*api.DescribeResponse, error) {
	return &api.DescribeResponse{Name: "testplugin"}, nil
}

func serveTestPlugin(ctx context.Context, options sandbox.RunOptions) {
	readerCloser, ok := options.Stdin.(io.ReadCloser)
	if !ok {
		readerCloser = io.NopCloser(options.Stdin)
	}
	writerCloser, ok := options.Stdout.(io.WriteCloser)
	if !ok {
		writerCloser = nopWriteCloser{Writer: options.Stdout}
	}
	listener := stdio.NewListener(readerCloser, writerCloser)
	server := grpc.NewServer()
	api.RegisterPluginServiceServer(server, &testPluginServer{})
	go func() {
		<-ctx.Done()
		server.Stop()
	}()
	_ = server.Serve(listener)
}

func TestStartPlugin_ImagePluginDescribes(t *testing.T) {
	tempDirectory := t.TempDir()
	sourceTree, err := sourcetree.Open(tempDirectory)
	if err != nil {
		t.Fatal(err)
	}
	scope := tasks.NewTaskContext(tempDirectory, tempDirectory, sourceTree)

	var receivedConfig sandbox.Config
	backend := &mockSandboxBackend{
		runFunction: func(ctx context.Context, config sandbox.Config, args []string, options sandbox.RunOptions) error {
			receivedConfig = config
			serveTestPlugin(ctx, options)
			return nil
		},
	}

	var stderr bytes.Buffer
	client, closer, err := plugins.StartPlugin(t.Context(), scope, backend, "ghcr.io/example/plugin@sha256:1234", false, &stderr)
	if err != nil {
		t.Fatalf("StartPlugin failed: %v", err)
	}
	defer closer.Close()

	response, err := client.Describe(t.Context(), &api.DescribeRequest{})
	if err != nil {
		t.Fatalf("Describe failed: %v", err)
	}
	if response.Name != "testplugin" {
		t.Errorf("expected plugin name 'testplugin', got %q", response.Name)
	}
	if receivedConfig.Network != "none" {
		t.Errorf("expected Network: none when network not allowed, got %q", receivedConfig.Network)
	}
	if receivedConfig.Image != "ghcr.io/example/plugin@sha256:1234" {
		t.Errorf("expected Image in config, got %q", receivedConfig.Image)
	}
}

func TestStartPlugin_NetworkIsolationError(t *testing.T) {
	tempDirectory := t.TempDir()
	sourceTree, err := sourcetree.Open(tempDirectory)
	if err != nil {
		t.Fatal(err)
	}
	scope := tasks.NewTaskContext(tempDirectory, tempDirectory, sourceTree)

	backend := &mockSandboxBackend{
		runFunction: func(ctx context.Context, config sandbox.Config, args []string, options sandbox.RunOptions) error {
			if config.Network != "" {
				return fmt.Errorf("network isolation is not supported by the proot sandbox")
			}
			return nil
		},
	}

	starter := plugins.NewSandboxStarter(backend)
	_, err = plugins.DescribeContainer(t.Context(), scope, t.TempDir(), "myplugin", "ghcr.io/example/plugin@sha256:1234", starter)
	if err == nil {
		t.Fatal("expected error when network isolation not supported, got nil")
	}
	errorMessage := err.Error()
	if !strings.Contains(errorMessage, "myplugin") {
		t.Errorf("expected error to name plugin, got: %v", err)
	}
	if !strings.Contains(errorMessage, "network isolation is not supported by the proot sandbox") {
		t.Errorf("expected error to contain library error, got: %v", err)
	}
}

func TestLoadContainerPlugins_DockerfileWithoutDocker(t *testing.T) {
	repoRoot := t.TempDir()
	apDirectory := filepath.Join(repoRoot, ".ap")
	if err := os.MkdirAll(apDirectory, 0755); err != nil {
		t.Fatal(err)
	}
	pluginsYAML := `
plugins:
  local-tool:
    dockerfile: images/local/Dockerfile
`
	if err := os.WriteFile(filepath.Join(apDirectory, "plugins.yaml"), []byte(pluginsYAML), 0644); err != nil {
		t.Fatal(err)
	}

	sourceTree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	nonexistentDocker := filepath.Join(t.TempDir(), "docker")
	scope := tasks.NewTaskContext(repoRoot, repoRoot, sourceTree).WithTool("docker", nonexistentDocker)

	t.Setenv("PATH", t.TempDir()) // Ensure docker executable is not found on PATH
	_, err = plugins.LoadContainerPlugins(t.Context(), scope, plugins.ContainerPluginOptions{
		CacheDirectory: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error for dockerfile plugin on host without docker, got nil")
	}
	errorMessage := err.Error()
	if !strings.Contains(errorMessage, `plugin "local-tool" requires docker:`) {
		t.Errorf("expected error to name plugin and state docker required, got: %s", errorMessage)
	}
}
