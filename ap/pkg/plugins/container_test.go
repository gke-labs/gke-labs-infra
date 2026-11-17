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
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins/api"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins/sdk"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins/stdio"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"google.golang.org/grpc"
)

type closerFunc func() error

func (closer closerFunc) Close() error {
	return closer()
}

func setupTestPluginStarter(t *testing.T, plugin sdk.Plugin) plugins.StartPluginFunc {
	t.Helper()
	return func(ctx context.Context, scope *tasks.TaskContext, imageRef string, allowNetwork bool, stderr io.Writer) (api.PluginServiceClient, io.Closer, error) {
		serverReader, clientWriter := io.Pipe()
		clientReader, serverWriter := io.Pipe()

		serverListener := stdio.NewListener(serverReader, serverWriter)
		clientConnection := stdio.NewConn(clientReader, clientWriter)

		serverDone := make(chan error, 1)
		go func() {
			serverDone <- sdk.Serve(serverListener, plugin)
		}()

		client, closer, err := plugins.DialStdioConn(ctx, clientConnection)
		if err != nil {
			_ = serverListener.Close()
			return nil, nil, err
		}

		wrappedCloser := closerFunc(func() error {
			return errors.Join(closer.Close(), serverListener.Close(), <-serverDone)
		})
		return client, wrappedCloser, nil
	}
}

func TestRunTask_InputAndOutput(t *testing.T) {
	repoRoot := t.TempDir()
	docsDir := filepath.Join(repoRoot, "docs")
	if err := os.MkdirAll(docsDir, 0755); err != nil {
		t.Fatal(err)
	}

	rootFile := filepath.Join(repoRoot, "root.txt")
	if err := os.WriteFile(rootFile, []byte("root file content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	inputFile := filepath.Join(docsDir, "input.txt")
	if err := os.WriteFile(inputFile, []byte("hello input\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	var stdoutBuffer, stderrBuffer bytes.Buffer
	scope := tasks.NewTaskContext(repoRoot, docsDir, tree.Under(docsDir)).WithOutput(&stdoutBuffer, &stderrBuffer)

	testPlugin := sdk.Plugin{
		Name:       "example",
		Activation: []string{"input.txt"},
		Tasks: []sdk.Task{
			{
				Name:        "append",
				Verb:        "build",
				Description: "Appends text to input file and verifies whole repository view",
				Outputs:     []string{"output.txt"},
				Run: func(ctx context.Context, taskContext sdk.TaskContext) error {
					rootData, readErr := os.ReadFile(filepath.Join(taskContext.RootDir, "root.txt"))
					if readErr != nil {
						return fmt.Errorf("reading root.txt from repository view: %w", readErr)
					}
					if string(rootData) != "root file content\n" {
						return fmt.Errorf("unexpected root.txt content: %q", string(rootData))
					}

					data, readErr := os.ReadFile(filepath.Join(taskContext.Dir, "input.txt"))
					if readErr != nil {
						return fmt.Errorf("reading input.txt: %w", readErr)
					}
					if _, printErr := fmt.Fprintln(taskContext.Stdout, "processed input.txt"); printErr != nil {
						return printErr
					}
					output := append(data, []byte("processed line\n")...)
					return os.WriteFile(filepath.Join(taskContext.OutputDir, "output.txt"), output, 0644)
				},
			},
		},
	}

	starter := setupTestPluginStarter(t, testPlugin)

	declaration := config.PluginDeclaration{Image: "example.com/mock@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"}
	manifest := &plugins.PluginManifest{
		Name:  "example",
		Tasks: []plugins.TaskMetadata{{Name: "append", Verb: "build", Outputs: []string{"output.txt"}}},
	}

	task := &plugins.ContainerTask{
		PluginName:        "example",
		TaskMetadata:      manifest.Tasks[0],
		Declaration:       declaration,
		ImageRef:          declaration.Image,
		Manifest:          manifest,
		Dir:               docsDir,
		RelativeDirectory: "docs",
		RepoRoot:          repoRoot,
		Starter:           starter,
	}

	if err := task.Run(t.Context(), scope); err != nil {
		t.Fatalf("task.Run failed: %v", err)
	}

	outputFilePath := filepath.Join(docsDir, "output.txt")
	gotContent, err := os.ReadFile(outputFilePath)
	if err != nil {
		t.Fatalf("reading output file: %v", err)
	}
	wantContent := "hello input\nprocessed line\n"
	if string(gotContent) != wantContent {
		t.Errorf("output file content = %q, want %q", string(gotContent), wantContent)
	}

	if !strings.Contains(stdoutBuffer.String(), "processed input.txt") {
		t.Errorf("expected stdout to contain 'processed input.txt', got: %s", stdoutBuffer.String())
	}
}

func TestRunTask_PathEscapesRejected(t *testing.T) {
	repoRoot := t.TempDir()
	docsDir := filepath.Join(repoRoot, "docs")
	if err := os.MkdirAll(docsDir, 0755); err != nil {
		t.Fatal(err)
	}

	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	scope := tasks.NewTaskContext(repoRoot, docsDir, tree.Under(docsDir))
	declaration := config.PluginDeclaration{Image: "example.com/mock@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"}

	t.Run("rejects output path with dot-dot escape", func(t *testing.T) {
		starter := func(ctx context.Context, taskScope *tasks.TaskContext, imageRef string, allowNetwork bool, stderr io.Writer) (api.PluginServiceClient, io.Closer, error) {
			serverReader, clientWriter := io.Pipe()
			clientReader, serverWriter := io.Pipe()
			serverListener := stdio.NewListener(serverReader, serverWriter)
			clientConnection := stdio.NewConn(clientReader, clientWriter)

			go func() {
				grpcServer := grpc.NewServer()
				api.RegisterPluginServiceServer(grpcServer, &escapingPluginServer{outputPath: "../evil.txt"})
				_ = grpcServer.Serve(serverListener)
			}()

			client, closer, err := plugins.DialStdioConn(ctx, clientConnection)
			if err != nil {
				return nil, nil, err
			}
			return client, closerFunc(func() error {
				return errors.Join(closer.Close(), serverListener.Close())
			}), nil
		}

		manifest := &plugins.PluginManifest{
			Name:  "mockplugin",
			Tasks: []plugins.TaskMetadata{{Name: "build", Verb: "build", Outputs: []string{"../evil.txt"}}},
		}
		task := &plugins.ContainerTask{
			PluginName:        "mockplugin",
			TaskMetadata:      manifest.Tasks[0],
			Declaration:       declaration,
			ImageRef:          declaration.Image,
			Manifest:          manifest,
			Dir:               docsDir,
			RelativeDirectory: "docs",
			RepoRoot:          repoRoot,
			Starter:           starter,
		}

		err := task.Run(t.Context(), scope)
		if err == nil {
			t.Fatal("expected task.Run to fail on dot-dot escape, got nil")
		}
		if !strings.Contains(err.Error(), "mockplugin") || !strings.Contains(err.Error(), "build") || !strings.Contains(err.Error(), docsDir) {
			t.Errorf("expected error to name plugin, task and dir, got: %v", err)
		}
	})

	t.Run("rejects output path under .git", func(t *testing.T) {
		starter := func(ctx context.Context, taskScope *tasks.TaskContext, imageRef string, allowNetwork bool, stderr io.Writer) (api.PluginServiceClient, io.Closer, error) {
			serverReader, clientWriter := io.Pipe()
			clientReader, serverWriter := io.Pipe()
			serverListener := stdio.NewListener(serverReader, serverWriter)
			clientConnection := stdio.NewConn(clientReader, clientWriter)

			go func() {
				grpcServer := grpc.NewServer()
				api.RegisterPluginServiceServer(grpcServer, &escapingPluginServer{outputPath: ".git/config"})
				_ = grpcServer.Serve(serverListener)
			}()

			client, closer, err := plugins.DialStdioConn(ctx, clientConnection)
			if err != nil {
				return nil, nil, err
			}
			return client, closerFunc(func() error {
				return errors.Join(closer.Close(), serverListener.Close())
			}), nil
		}

		manifest := &plugins.PluginManifest{
			Name:  "mockplugin",
			Tasks: []plugins.TaskMetadata{{Name: "build", Verb: "build", Outputs: []string{".git/config"}}},
		}
		task := &plugins.ContainerTask{
			PluginName:        "mockplugin",
			TaskMetadata:      manifest.Tasks[0],
			Declaration:       declaration,
			ImageRef:          declaration.Image,
			Manifest:          manifest,
			Dir:               docsDir,
			RelativeDirectory: "docs",
			RepoRoot:          repoRoot,
			Starter:           starter,
		}

		err := task.Run(t.Context(), scope)
		if err == nil {
			t.Fatal("expected task.Run to fail on .git path, got nil")
		}
		if !strings.Contains(err.Error(), "mockplugin") || !strings.Contains(err.Error(), "build") || !strings.Contains(err.Error(), docsDir) {
			t.Errorf("expected error to name plugin, task and dir, got: %v", err)
		}
	})

	t.Run("rejects writing through symlink in destination directory", func(t *testing.T) {
		outsideDir := t.TempDir()
		outsideTarget := filepath.Join(outsideDir, "target.txt")
		if err := os.WriteFile(outsideTarget, []byte("secret"), 0644); err != nil {
			t.Fatal(err)
		}

		symlinkPath := filepath.Join(docsDir, "symlink_file.txt")
		_ = os.Remove(symlinkPath)
		if err := os.Symlink(outsideTarget, symlinkPath); err != nil {
			t.Fatal(err)
		}

		testPlugin := sdk.Plugin{
			Name: "mockplugin",
			Tasks: []sdk.Task{
				{
					Name:        "build",
					Verb:        "build",
					Description: "Writes to symlink",
					Outputs:     []string{"symlink_file.txt"},
					Run: func(ctx context.Context, taskContext sdk.TaskContext) error {
						return os.WriteFile(filepath.Join(taskContext.OutputDir, "symlink_file.txt"), []byte("tampered"), 0644)
					},
				},
			},
		}

		starter := setupTestPluginStarter(t, testPlugin)
		manifest := &plugins.PluginManifest{
			Name:  "mockplugin",
			Tasks: []plugins.TaskMetadata{{Name: "build", Verb: "build", Outputs: []string{"symlink_file.txt"}}},
		}
		task := &plugins.ContainerTask{
			PluginName:        "mockplugin",
			TaskMetadata:      manifest.Tasks[0],
			Declaration:       declaration,
			ImageRef:          declaration.Image,
			Manifest:          manifest,
			Dir:               docsDir,
			RelativeDirectory: "docs",
			RepoRoot:          repoRoot,
			Starter:           starter,
		}

		err := task.Run(t.Context(), scope)
		if err == nil {
			t.Fatal("expected task.Run to fail writing through symlink, got nil")
		}

		outsideData, readErr := os.ReadFile(outsideTarget)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(outsideData) != "secret" {
			t.Errorf("target file was modified through symlink: got %q, want 'secret'", string(outsideData))
		}
	})
}

type escapingPluginServer struct {
	api.UnimplementedPluginServiceServer
	outputPath string
}

func (server *escapingPluginServer) Describe(_ context.Context, _ *api.DescribeRequest) (*api.DescribeResponse, error) {
	return &api.DescribeResponse{Name: "mock"}, nil
}

func (server *escapingPluginServer) RunTask(stream api.PluginService_RunTaskServer) error {
	for {
		_, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}

	_ = stream.Send(&api.RunTaskResponse{
		Payload: &api.RunTaskResponse_OutputFile{
			OutputFile: &api.FileChunk{
				Path:        server.outputPath,
				Mode:        0644,
				Content:     []byte("escape content"),
				IsLastChunk: true,
			},
		},
	})

	return stream.Send(&api.RunTaskResponse{
		Payload: &api.RunTaskResponse_Result{
			Result: &api.TaskResult{ExitCode: 0},
		},
	})
}

func TestRunTask_FailedTaskResult(t *testing.T) {
	repoRoot := t.TempDir()
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	scope := tasks.NewTaskContext(repoRoot, repoRoot, tree)

	testPlugin := sdk.Plugin{
		Name: "failplugin",
		Tasks: []sdk.Task{
			{
				Name: "failtask",
				Verb: "test",
				Run: func(ctx context.Context, taskContext sdk.TaskContext) error {
					return errors.New("simulated task error")
				},
			},
		},
	}

	starter := setupTestPluginStarter(t, testPlugin)

	declaration := config.PluginDeclaration{Image: "example.com/mock@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"}
	manifest := &plugins.PluginManifest{
		Name:  "failplugin",
		Tasks: []plugins.TaskMetadata{{Name: "failtask", Verb: "test"}},
	}

	task := &plugins.ContainerTask{
		PluginName:        "failplugin",
		TaskMetadata:      manifest.Tasks[0],
		Declaration:       declaration,
		ImageRef:          declaration.Image,
		Manifest:          manifest,
		Dir:               repoRoot,
		RelativeDirectory: ".",
		RepoRoot:          repoRoot,
		Starter:           starter,
	}

	err = task.Run(t.Context(), scope)
	if err == nil {
		t.Fatal("expected task.Run to fail, got nil")
	}

	errorString := err.Error()
	if !strings.Contains(errorString, "failplugin") || !strings.Contains(errorString, "failtask") || !strings.Contains(errorString, repoRoot) {
		t.Errorf("expected error to name plugin, task, and dir, got: %s", errorString)
	}
	if !strings.Contains(errorString, "simulated task error") {
		t.Errorf("expected error to contain simulated error message, got: %s", errorString)
	}
}

func TestRunTask_EarlyExitWithoutResult(t *testing.T) {
	repoRoot := t.TempDir()
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	scope := tasks.NewTaskContext(repoRoot, repoRoot, tree)

	starter := func(ctx context.Context, taskScope *tasks.TaskContext, imageRef string, allowNetwork bool, stderr io.Writer) (api.PluginServiceClient, io.Closer, error) {
		serverReader, clientWriter := io.Pipe()
		clientReader, serverWriter := io.Pipe()
		serverListener := stdio.NewListener(serverReader, serverWriter)
		clientConnection := stdio.NewConn(clientReader, clientWriter)

		go func() {
			grpcServer := grpc.NewServer()
			api.RegisterPluginServiceServer(grpcServer, &earlyExitPluginServer{})
			_ = grpcServer.Serve(serverListener)
		}()

		client, closer, err := plugins.DialStdioConn(ctx, clientConnection)
		if err != nil {
			return nil, nil, err
		}
		return client, closerFunc(func() error {
			return errors.Join(closer.Close(), serverListener.Close())
		}), nil
	}

	declaration := config.PluginDeclaration{Image: "example.com/mock@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"}
	manifest := &plugins.PluginManifest{
		Name:  "crashplugin",
		Tasks: []plugins.TaskMetadata{{Name: "crashtask", Verb: "test"}},
	}

	task := &plugins.ContainerTask{
		PluginName:        "crashplugin",
		TaskMetadata:      manifest.Tasks[0],
		Declaration:       declaration,
		ImageRef:          declaration.Image,
		Manifest:          manifest,
		Dir:               repoRoot,
		RelativeDirectory: ".",
		RepoRoot:          repoRoot,
		Starter:           starter,
	}

	err = task.Run(t.Context(), scope)
	if err == nil {
		t.Fatal("expected task.Run to fail on early exit, got nil")
	}
	errorString := err.Error()
	if !strings.Contains(errorString, "crashplugin") || !strings.Contains(errorString, "crashtask") || !strings.Contains(errorString, repoRoot) {
		t.Errorf("expected error to name plugin, task, and dir, got: %s", errorString)
	}
	if !strings.Contains(errorString, "exited early without returning a result") {
		t.Errorf("expected error to mention early exit, got: %s", errorString)
	}
}

type earlyExitPluginServer struct {
	api.UnimplementedPluginServiceServer
}

func (server *earlyExitPluginServer) Describe(_ context.Context, _ *api.DescribeRequest) (*api.DescribeResponse, error) {
	return &api.DescribeResponse{Name: "crashplugin"}, nil
}

func (server *earlyExitPluginServer) RunTask(stream api.PluginService_RunTaskServer) error {
	for {
		_, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func TestVerifyMode_ReportsDifferenceWithoutTouchingCheckout(t *testing.T) {
	repoRoot := t.TempDir()

	initialContent := "// Code generated by example plugin. DO NOT EDIT.\nOriginal pristine content\n"
	genFile := filepath.Join(repoRoot, "generated.go")
	if err := os.WriteFile(genFile, []byte(initialContent), 0644); err != nil {
		t.Fatal(err)
	}

	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	var stderrBuffer bytes.Buffer
	scope := tasks.NewTaskContext(repoRoot, repoRoot, tree).WithVerify(true).WithOutput(io.Discard, &stderrBuffer)

	testPlugin := sdk.Plugin{
		Name: "genplugin",
		Tasks: []sdk.Task{
			{
				Name:        "generate",
				Verb:        "generate",
				Description: "Generates output",
				Outputs:     []string{"generated.go"},
				Run: func(ctx context.Context, taskContext sdk.TaskContext) error {
					diffContent := "// Code generated by example plugin. DO NOT EDIT.\nDifferent modified content\n"
					return os.WriteFile(filepath.Join(taskContext.OutputDir, "generated.go"), []byte(diffContent), 0644)
				},
			},
		},
	}

	starter := setupTestPluginStarter(t, testPlugin)

	declaration := config.PluginDeclaration{Image: "example.com/mock@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"}
	manifest := &plugins.PluginManifest{
		Name:  "genplugin",
		Tasks: []plugins.TaskMetadata{{Name: "generate", Verb: "generate", Outputs: []string{"generated.go"}}},
	}

	task := &plugins.ContainerTask{
		PluginName:        "genplugin",
		TaskMetadata:      manifest.Tasks[0],
		Declaration:       declaration,
		ImageRef:          declaration.Image,
		Manifest:          manifest,
		Dir:               repoRoot,
		RelativeDirectory: ".",
		RepoRoot:          repoRoot,
		Starter:           starter,
	}

	runErr := task.Run(t.Context(), scope)
	if runErr == nil {
		t.Fatal("expected task.Run to fail in verify mode when generated file differs, got nil")
	}

	if !strings.Contains(runErr.Error(), "outputs differ from checkout") {
		t.Errorf("expected error to mention diff, got: %v", runErr)
	}

	gotData, readErr := os.ReadFile(genFile)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(gotData) != initialContent {
		t.Errorf("checkout was touched after failed verify:\nGot:  %q\nWant: %q", string(gotData), initialContent)
	}
}

func TestSymlinkRuleInRepositoryView(t *testing.T) {
	repoRoot := t.TempDir()

	realFile := filepath.Join(repoRoot, "real.txt")
	if err := os.WriteFile(realFile, []byte("real content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	validLink := filepath.Join(repoRoot, "link_to_real.txt")
	if err := os.Symlink("real.txt", validLink); err != nil {
		t.Fatal(err)
	}

	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	scope := tasks.NewTaskContext(repoRoot, repoRoot, tree)

	var receivedFiles []string
	testPlugin := sdk.Plugin{
		Name: "linkplugin",
		Tasks: []sdk.Task{
			{
				Name: "check-link",
				Verb: "test",
				Run: func(ctx context.Context, taskContext sdk.TaskContext) error {
					data, readErr := os.ReadFile(filepath.Join(taskContext.Dir, "link_to_real.txt"))
					if readErr != nil {
						return readErr
					}
					if string(data) != "real content\n" {
						return fmt.Errorf("unexpected content: %q", string(data))
					}
					receivedFiles = append(receivedFiles, "link_to_real.txt")
					return nil
				},
			},
		},
	}

	starter := setupTestPluginStarter(t, testPlugin)

	declaration := config.PluginDeclaration{Image: "example.com/mock@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"}
	manifest := &plugins.PluginManifest{
		Name:  "linkplugin",
		Tasks: []plugins.TaskMetadata{{Name: "check-link", Verb: "test"}},
	}

	task := &plugins.ContainerTask{
		PluginName:        "linkplugin",
		TaskMetadata:      manifest.Tasks[0],
		Declaration:       declaration,
		ImageRef:          declaration.Image,
		Manifest:          manifest,
		Dir:               repoRoot,
		RelativeDirectory: ".",
		RepoRoot:          repoRoot,
		Starter:           starter,
	}

	if err := task.Run(t.Context(), scope); err != nil {
		t.Fatalf("task.Run failed: %v", err)
	}
	if len(receivedFiles) != 1 {
		t.Fatalf("expected 1 received file through symlink, got %d", len(receivedFiles))
	}
}

func TestRunTask_DanglingSymlink(t *testing.T) {
	repoRoot := t.TempDir()

	realFile := filepath.Join(repoRoot, "real.txt")
	if err := os.WriteFile(realFile, []byte("real content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	danglingLink := filepath.Join(repoRoot, "dangling.txt")
	if err := os.Symlink("missing.txt", danglingLink); err != nil {
		t.Fatal(err)
	}

	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	scope := tasks.NewTaskContext(repoRoot, repoRoot, tree)

	testPlugin := sdk.Plugin{
		Name: "linkplugin",
		Tasks: []sdk.Task{
			{
				Name: "check-link",
				Verb: "test",
				Run: func(ctx context.Context, taskContext sdk.TaskContext) error {
					return nil
				},
			},
		},
	}

	starter := setupTestPluginStarter(t, testPlugin)

	declaration := config.PluginDeclaration{Image: "example.com/mock@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"}
	manifest := &plugins.PluginManifest{
		Name:  "linkplugin",
		Tasks: []plugins.TaskMetadata{{Name: "check-link", Verb: "test"}},
	}

	task := &plugins.ContainerTask{
		PluginName:        "linkplugin",
		TaskMetadata:      manifest.Tasks[0],
		Declaration:       declaration,
		ImageRef:          declaration.Image,
		Manifest:          manifest,
		Dir:               repoRoot,
		RelativeDirectory: ".",
		RepoRoot:          repoRoot,
		Starter:           starter,
	}

	testContext, cancelTest := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelTest()

	err = task.Run(testContext, scope)
	if err == nil {
		t.Fatal("expected error for dangling symlink, got nil")
	}
	if !strings.Contains(err.Error(), "dangling.txt") {
		t.Errorf("expected error naming 'dangling.txt', got: %v", err)
	}
}

func TestRunTask_SymlinkedRepoRoot(t *testing.T) {
	tempBaseDirectory := t.TempDir()
	actualParentDirectory := filepath.Join(tempBaseDirectory, "actual-parent")
	if err := os.MkdirAll(actualParentDirectory, 0755); err != nil {
		t.Fatal(err)
	}

	symlinkedParentDirectory := filepath.Join(tempBaseDirectory, "symlink-parent")
	if err := os.Symlink(actualParentDirectory, symlinkedParentDirectory); err != nil {
		t.Fatal(err)
	}

	repoRoot := filepath.Join(symlinkedParentDirectory, "repo")
	if err := os.MkdirAll(repoRoot, 0755); err != nil {
		t.Fatal(err)
	}

	realFile := filepath.Join(repoRoot, "hello.txt")
	if err := os.WriteFile(realFile, []byte("hello from actual\n"), 0644); err != nil {
		t.Fatal(err)
	}

	validLink := filepath.Join(repoRoot, "link.txt")
	if err := os.Symlink("hello.txt", validLink); err != nil {
		t.Fatal(err)
	}

	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	scope := tasks.NewTaskContext(repoRoot, repoRoot, tree)

	var receivedFiles []string
	testPlugin := sdk.Plugin{
		Name: "symlink-root-plugin",
		Tasks: []sdk.Task{
			{
				Name:    "read-files",
				Verb:    "build",
				Outputs: []string{"output.txt"},
				Run: func(ctx context.Context, taskContext sdk.TaskContext) error {
					firstData, readError := os.ReadFile(filepath.Join(taskContext.Dir, "hello.txt"))
					if readError != nil {
						return readError
					}
					secondData, readError := os.ReadFile(filepath.Join(taskContext.Dir, "link.txt"))
					if readError != nil {
						return readError
					}
					if string(firstData) != "hello from actual\n" || string(secondData) != "hello from actual\n" {
						return fmt.Errorf("unexpected content: %q, %q", string(firstData), string(secondData))
					}
					receivedFiles = append(receivedFiles, "hello.txt", "link.txt")
					return os.WriteFile(filepath.Join(taskContext.OutputDir, "output.txt"), []byte("success"), 0644)
				},
			},
		},
	}

	starter := setupTestPluginStarter(t, testPlugin)

	declaration := config.PluginDeclaration{Image: "example.com/mock@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"}
	manifest := &plugins.PluginManifest{
		Name:  "symlink-root-plugin",
		Tasks: []plugins.TaskMetadata{{Name: "read-files", Verb: "build", Outputs: []string{"output.txt"}}},
	}

	task := &plugins.ContainerTask{
		PluginName:        "symlink-root-plugin",
		TaskMetadata:      manifest.Tasks[0],
		Declaration:       declaration,
		ImageRef:          declaration.Image,
		Manifest:          manifest,
		Dir:               repoRoot,
		RelativeDirectory: ".",
		RepoRoot:          repoRoot,
		Starter:           starter,
	}

	if err := task.Run(t.Context(), scope); err != nil {
		t.Fatalf("task.Run failed: %v", err)
	}
	if len(receivedFiles) != 2 {
		t.Fatalf("expected 2 received files, got %d", len(receivedFiles))
	}
	outputContent, err := os.ReadFile(filepath.Join(repoRoot, "output.txt"))
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}
	if string(outputContent) != "success" {
		t.Errorf("output = %q, want 'success'", string(outputContent))
	}
}

func TestNetworkAuthorization(t *testing.T) {
	repoRoot := t.TempDir()
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	scope := tasks.NewTaskContext(repoRoot, repoRoot, tree)
	declaration := config.PluginDeclaration{
		Image:   "example.com/mock@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		Network: false,
	}
	manifest := &plugins.PluginManifest{
		Name:    "mockplugin",
		Network: true,
		Tasks:   []plugins.TaskMetadata{{Name: "fetch", Verb: "test"}},
	}

	task := &plugins.ContainerTask{
		PluginName:   "mockplugin",
		TaskMetadata: manifest.Tasks[0],
		Declaration:  declaration,
		ImageRef:     declaration.Image,
		Manifest:     manifest,
		Dir:          repoRoot,
		RepoRoot:     repoRoot,
	}

	err = task.Run(t.Context(), scope)
	if err == nil {
		t.Fatal("expected error when network requested without consent in .ap/plugins.yaml")
	}
	if !strings.Contains(err.Error(), "requires network access, but network is not authorized") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func setupFakeDocker(t *testing.T) (string, string) {
	t.Helper()
	stateDir := t.TempDir()
	fakeBinDir := t.TempDir()

	script := fmt.Sprintf(`#!/bin/sh
STATE_DIR=%q

case "$1" in
  build)
    if [ -f "$STATE_DIR/fail-build" ]; then
      echo "simulated build failure" >&2
      exit 42
    fi
    echo "$*" > "$STATE_DIR/build-args"
    ;;
  load)
    echo "$*" > "$STATE_DIR/load-args"
    ;;
  image)
    if [ "$2" = "inspect" ]; then
      if [ -f "$STATE_DIR/buildctl-args" ]; then
        echo "sha256:fake-buildctl-image-5678"
      else
        echo "sha256:fake-built-image-1234"
      fi
    fi
    ;;
esac
`, stateDir)

	fakeDockerPath := filepath.Join(fakeBinDir, "docker")
	if err := os.WriteFile(fakeDockerPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	fakeBuildxPath := filepath.Join(fakeBinDir, "docker-buildx")
	if err := os.WriteFile(fakeBuildxPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	buildctlScript := fmt.Sprintf(`#!/bin/sh
STATE_DIR=%q

if [ -f "$STATE_DIR/fail-buildctl" ]; then
  echo "simulated buildctl failure" >&2
  exit 42
fi

echo "$*" > "$STATE_DIR/buildctl-args"
while [ $# -gt 0 ]; do
  case "$1" in
    --metadata-file)
      cat > "$2" <<'EOF'
{
  "containerimage.digest": "sha256:fake-buildctl-image-5678"
}
EOF
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
`, stateDir)

	fakeBuildctlPath := filepath.Join(fakeBinDir, "buildctl")
	if err := os.WriteFile(fakeBuildctlPath, []byte(buildctlScript), 0755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", fakeBinDir+":"+os.Getenv("PATH"))
	return fakeBinDir, stateDir
}

func TestDockerfilePlugin(t *testing.T) {
	repoRoot := t.TempDir()
	cacheDirectory := t.TempDir()

	apDirectory := filepath.Join(repoRoot, ".ap")
	if err := os.MkdirAll(apDirectory, 0755); err != nil {
		t.Fatal(err)
	}
	pluginsContent := `
plugins:
  mockplugin:
    dockerfile: images/mock/Dockerfile
`
	if err := os.WriteFile(filepath.Join(apDirectory, "plugins.yaml"), []byte(pluginsContent), 0644); err != nil {
		t.Fatal(err)
	}

	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	fakeBinDir, stateDir := setupFakeDocker(t)
	fakeDockerPath := filepath.Join(fakeBinDir, "docker")
	fakeBuildxPath := filepath.Join(fakeBinDir, "docker-buildx")
	scope := tasks.NewTaskContext(repoRoot, repoRoot, tree).WithTool("docker", fakeDockerPath).WithTool("docker-buildx", fakeBuildxPath)

	mockPlugin := sdk.Plugin{
		Name:       "mockplugin",
		Activation: []string{"mock.config"},
		Network:    false,
		Tasks: []sdk.Task{
			{Name: "build", Verb: "build", Outputs: []string{"dist"}},
		},
	}
	starter := setupTestPluginStarter(t, mockPlugin)

	t.Run("build invoked with expected arguments and image ID becomes cache key", func(t *testing.T) {
		options := plugins.ContainerPluginOptions{
			CacheDirectory: cacheDirectory,
			Starter:        starter,
			BuildImage:     plugins.BuildPluginImage,
			ImageLookup:    plugins.LookupDockerImage,
		}
		loadedPlugins, err := plugins.LoadContainerPlugins(t.Context(), scope, options)
		if err != nil {
			t.Fatalf("LoadContainerPlugins failed: %v", err)
		}
		if len(loadedPlugins) != 1 {
			t.Fatalf("expected 1 plugin, got %d", len(loadedPlugins))
		}

		containerPlugin, ok := loadedPlugins[0].(*plugins.ContainerPlugin)
		if !ok {
			t.Fatalf("expected *plugins.ContainerPlugin, got %T", loadedPlugins[0])
		}
		if containerPlugin.Declaration.Image != "" {
			t.Errorf("expected Declaration.Image to remain unset, got %q", containerPlugin.Declaration.Image)
		}
		if containerPlugin.ImageRef != "sha256:fake-built-image-1234" {
			t.Errorf("expected ImageRef %q, got %q", "sha256:fake-built-image-1234", containerPlugin.ImageRef)
		}

		buildArgsData, err := os.ReadFile(filepath.Join(stateDir, "build-args"))
		if err != nil {
			t.Fatalf("reading build args: %v", err)
		}
		buildArgs := string(buildArgsData)
		if !strings.HasPrefix(buildArgs, "build ") {
			t.Errorf("expected build command, got: %s", buildArgs)
		}
		if !strings.Contains(buildArgs, "-f images/mock/Dockerfile") {
			t.Errorf("expected -f images/mock/Dockerfile in args, got: %s", buildArgs)
		}
		if !strings.Contains(buildArgs, "-t ap-plugin/mockplugin:latest") {
			t.Errorf("expected -t ap-plugin/mockplugin:latest in args, got: %s", buildArgs)
		}
		if !strings.Contains(buildArgs, "--load") {
			t.Errorf("expected --load in args, got: %s", buildArgs)
		}
		if !strings.HasSuffix(strings.TrimSpace(buildArgs), ".") {
			t.Errorf("expected build context . at end of args, got: %s", buildArgs)
		}

		cachedFile := filepath.Join(cacheDirectory, "plugins", "describe", "sha256-fake-built-image-1234.json")
		if _, err := os.Stat(cachedFile); err != nil {
			t.Errorf("expected cached manifest at %s: %v", cachedFile, err)
		}

		// Remove build-args to detect if build is executed on second run
		buildArgsPath := filepath.Join(stateDir, "build-args")
		if err := os.Remove(buildArgsPath); err != nil {
			t.Fatalf("removing build-args: %v", err)
		}

		// Second invocation with unchanged inputs: uses cached image ID without rebuilding
		secondPlugins, err := plugins.LoadContainerPlugins(t.Context(), scope, options)
		if err != nil {
			t.Fatalf("second LoadContainerPlugins failed: %v", err)
		}
		if len(secondPlugins) != 1 {
			t.Fatalf("expected 1 plugin on second run, got %d", len(secondPlugins))
		}
		secondContainerPlugin, ok := secondPlugins[0].(*plugins.ContainerPlugin)
		if !ok {
			t.Fatalf("expected *plugins.ContainerPlugin, got %T", secondPlugins[0])
		}
		if secondContainerPlugin.ImageRef != "sha256:fake-built-image-1234" {
			t.Errorf("expected cached ImageRef %q, got %q", "sha256:fake-built-image-1234", secondContainerPlugin.ImageRef)
		}
		if _, err := os.Stat(buildArgsPath); !os.IsNotExist(err) {
			t.Errorf("expected build to not run on second invocation, but build-args was re-created")
		}
	})

	t.Run("with buildkit host set build goes to buildctl and result is loaded", func(t *testing.T) {
		buildkitCacheDirectory := t.TempDir()
		loadedPlugins, err := plugins.LoadContainerPlugins(t.Context(), scope, plugins.ContainerPluginOptions{
			CacheDirectory: buildkitCacheDirectory,
			BuildkitHost:   "tcp://127.0.0.1:2375",
			Starter:        starter,
			BuildImage:     plugins.BuildPluginImage,
			ImageLookup:    plugins.LookupDockerImage,
		})
		if err != nil {
			t.Fatalf("LoadContainerPlugins failed: %v", err)
		}
		if len(loadedPlugins) != 1 {
			t.Fatalf("expected 1 plugin, got %d", len(loadedPlugins))
		}

		containerPlugin, ok := loadedPlugins[0].(*plugins.ContainerPlugin)
		if !ok {
			t.Fatalf("expected *plugins.ContainerPlugin, got %T", loadedPlugins[0])
		}
		if containerPlugin.ImageRef != "sha256:fake-buildctl-image-5678" {
			t.Errorf("expected ImageRef %q, got %q", "sha256:fake-buildctl-image-5678", containerPlugin.ImageRef)
		}

		buildctlArgsData, err := os.ReadFile(filepath.Join(stateDir, "buildctl-args"))
		if err != nil {
			t.Fatalf("reading buildctl args: %v", err)
		}
		buildctlArgs := string(buildctlArgsData)
		if !strings.HasPrefix(buildctlArgs, "build ") {
			t.Errorf("expected buildctl build command, got: %s", buildctlArgs)
		}
		if !strings.Contains(buildctlArgs, "--opt filename=images/mock/Dockerfile") {
			t.Errorf("expected filename=images/mock/Dockerfile in args, got: %s", buildctlArgs)
		}
		if !strings.Contains(buildctlArgs, "--output type=docker,name=ap-plugin/mockplugin:latest,dest=") {
			t.Errorf("expected type=docker output in args, got: %s", buildctlArgs)
		}

		loadArgsData, err := os.ReadFile(filepath.Join(stateDir, "load-args"))
		if err != nil {
			t.Fatalf("reading load args: %v", err)
		}
		loadArgs := string(loadArgsData)
		if !strings.Contains(loadArgs, "load -i") {
			t.Errorf("expected load -i in args, got: %s", loadArgs)
		}

		cachedFile := filepath.Join(buildkitCacheDirectory, "plugins", "describe", "sha256-fake-buildctl-image-5678.json")
		if _, err := os.Stat(cachedFile); err != nil {
			t.Errorf("expected cached manifest at %s: %v", cachedFile, err)
		}
	})

	t.Run("failing build is reported", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(stateDir, "fail-build"), []byte("fail"), 0644); err != nil {
			t.Fatal(err)
		}
		emptyCacheDirectory := t.TempDir()
		_, err := plugins.LoadContainerPlugins(t.Context(), scope, plugins.ContainerPluginOptions{
			CacheDirectory: emptyCacheDirectory,
			Starter:        starter,
			BuildImage:     plugins.BuildPluginImage,
			ImageLookup:    plugins.LookupDockerImage,
		})
		if err == nil {
			t.Fatal("expected LoadContainerPlugins to fail when build fails")
		}
		if !strings.Contains(err.Error(), "mockplugin") {
			t.Errorf("expected error to name plugin mockplugin, got: %v", err)
		}
		if !strings.Contains(err.Error(), "images/mock/Dockerfile") {
			t.Errorf("expected error to name Dockerfile images/mock/Dockerfile, got: %v", err)
		}
		if !strings.Contains(err.Error(), "simulated build failure") {
			t.Errorf("expected build output in error, got: %v", err)
		}
	})
}

func TestDockerfilePlugin_URL(t *testing.T) {
	remoteRepoDir, runRemoteGit := setupTestGitRepo(t)
	remoteDockerfile := filepath.Join(remoteRepoDir, "images/urlplugin/Dockerfile")
	if err := os.MkdirAll(filepath.Dir(remoteDockerfile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remoteDockerfile, []byte("FROM alpine\nRUN echo urlplugin\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runRemoteGit("add", "-A")
	runRemoteGit("commit", "-m", "initial remote commit")

	declaringRepoRoot := t.TempDir()
	cacheDirectory := t.TempDir()
	t.Setenv("AP_CACHE_DIR", cacheDirectory)

	apDirectory := filepath.Join(declaringRepoRoot, ".ap")
	if err := os.MkdirAll(apDirectory, 0755); err != nil {
		t.Fatal(err)
	}
	urlTarget := "file://" + remoteRepoDir + "//images/urlplugin/Dockerfile?ref=main"
	pluginsContent := fmt.Sprintf(`
plugins:
  urlplugin:
    dockerfile: %s
`, urlTarget)
	if err := os.WriteFile(filepath.Join(apDirectory, "plugins.yaml"), []byte(pluginsContent), 0644); err != nil {
		t.Fatal(err)
	}

	tree, err := sourcetree.Open(declaringRepoRoot)
	if err != nil {
		t.Fatal(err)
	}

	fakeBinDir, stateDir := setupFakeDocker(t)
	fakeDockerPath := filepath.Join(fakeBinDir, "docker")
	fakeBuildxPath := filepath.Join(fakeBinDir, "docker-buildx")
	scope := tasks.NewTaskContext(declaringRepoRoot, declaringRepoRoot, tree).WithTool("docker", fakeDockerPath).WithTool("docker-buildx", fakeBuildxPath)

	mockPlugin := sdk.Plugin{
		Name:       "urlplugin",
		Activation: []string{"url.config"},
		Network:    false,
		Tasks: []sdk.Task{
			{Name: "build", Verb: "build", Outputs: []string{"dist"}},
		},
	}
	starter := setupTestPluginStarter(t, mockPlugin)

	options := plugins.ContainerPluginOptions{
		CacheDirectory: cacheDirectory,
		Starter:        starter,
		BuildImage:     plugins.BuildPluginImage,
		ImageLookup:    plugins.LookupDockerImage,
	}

	loadedPlugins, err := plugins.LoadContainerPlugins(t.Context(), scope, options)
	if err != nil {
		t.Fatalf("LoadContainerPlugins failed: %v", err)
	}
	if len(loadedPlugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(loadedPlugins))
	}

	containerPlugin, ok := loadedPlugins[0].(*plugins.ContainerPlugin)
	if !ok {
		t.Fatalf("expected *plugins.ContainerPlugin, got %T", loadedPlugins[0])
	}
	if containerPlugin.ImageRef != "sha256:fake-built-image-1234" {
		t.Errorf("expected ImageRef 'sha256:fake-built-image-1234', got %q", containerPlugin.ImageRef)
	}
	if containerPlugin.RepoRoot != declaringRepoRoot {
		t.Errorf("expected plugin RepoRoot %q, got %q", declaringRepoRoot, containerPlugin.RepoRoot)
	}

	buildArgsData, err := os.ReadFile(filepath.Join(stateDir, "build-args"))
	if err != nil {
		t.Fatalf("reading build args: %v", err)
	}
	buildArgs := string(buildArgsData)
	if !strings.Contains(buildArgs, "-f images/urlplugin/Dockerfile") {
		t.Errorf("expected -f images/urlplugin/Dockerfile in build args, got: %s", buildArgs)
	}
	if !strings.Contains(buildArgs, "-t ap-plugin/urlplugin:latest") {
		t.Errorf("expected -t ap-plugin/urlplugin:latest in build args, got: %s", buildArgs)
	}

	// Remove build-args to verify second call reuses cache
	if err := os.Remove(filepath.Join(stateDir, "build-args")); err != nil {
		t.Fatal(err)
	}

	secondPlugins, err := plugins.LoadContainerPlugins(t.Context(), scope, options)
	if err != nil {
		t.Fatalf("second LoadContainerPlugins failed: %v", err)
	}
	if len(secondPlugins) != 1 {
		t.Fatalf("expected 1 plugin on second load, got %d", len(secondPlugins))
	}
	secondPlugin := secondPlugins[0].(*plugins.ContainerPlugin)
	if secondPlugin.ImageRef != "sha256:fake-built-image-1234" {
		t.Errorf("expected cached ImageRef 'sha256:fake-built-image-1234', got %q", secondPlugin.ImageRef)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "build-args")); !os.IsNotExist(err) {
		t.Errorf("expected build not to run on second invocation, but build-args was recreated")
	}
}

func TestContainerTask_GetName(t *testing.T) {
	tests := []struct {
		relativeDirectory string
		want              string
	}{
		{relativeDirectory: ".", want: "astro-build"},
		{relativeDirectory: "docs", want: "astro-build docs"},
		{relativeDirectory: "blog", want: "astro-build blog"},
	}
	names := make(map[string]bool)
	for _, testCase := range tests {
		task := &plugins.ContainerTask{
			PluginName:        "astro",
			TaskMetadata:      plugins.TaskMetadata{Name: "build"},
			RelativeDirectory: testCase.relativeDirectory,
		}
		got := task.GetName()
		if got != testCase.want {
			t.Errorf("task.GetName() for relative directory %q = %q, want %q", testCase.relativeDirectory, got, testCase.want)
		}
		names[got] = true
	}
	if len(names) != len(tests) {
		t.Errorf("expected %d distinct names, got %d", len(tests), len(names))
	}
}

func runVerifyTask(t *testing.T, outputs []string, repositoryFiles, mockFiles map[string]string) (string, error) {
	t.Helper()
	repoRoot := t.TempDir()
	docsDir := filepath.Join(repoRoot, "docs")
	for relativePath, content := range repositoryFiles {
		fullPath := filepath.Join(docsDir, relativePath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	testPlugin := sdk.Plugin{
		Name: "mockplugin",
		Tasks: []sdk.Task{
			{
				Name:        "generate",
				Verb:        "generate",
				Description: "Generate task",
				Outputs:     outputs,
				Run: func(ctx context.Context, taskContext sdk.TaskContext) error {
					for relativePath, content := range mockFiles {
						outputPath := filepath.Join(taskContext.OutputDir, relativePath)
						if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
							return err
						}
						if err := os.WriteFile(outputPath, []byte(content), 0644); err != nil {
							return err
						}
					}
					return nil
				},
			},
		},
	}

	starter := setupTestPluginStarter(t, testPlugin)

	var stderr bytes.Buffer
	scope := tasks.NewTaskContext(repoRoot, docsDir, tree.Under(docsDir)).
		WithOutput(io.Discard, &stderr).
		WithVerify(true)
	declaration := config.PluginDeclaration{Image: "example.com/mock@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"}
	manifest := &plugins.PluginManifest{
		Name:  "mockplugin",
		Tasks: []plugins.TaskMetadata{{Name: "generate", Verb: "generate", Outputs: outputs}},
	}
	task := &plugins.ContainerTask{
		PluginName:        "mockplugin",
		TaskMetadata:      manifest.Tasks[0],
		Declaration:       declaration,
		ImageRef:          declaration.Image,
		Manifest:          manifest,
		Dir:               docsDir,
		RelativeDirectory: "docs",
		RepoRoot:          repoRoot,
		Starter:           starter,
	}

	err = task.Run(t.Context(), scope)
	return stderr.String(), err
}

func TestContainerVerifyMode(t *testing.T) {
	t.Run("identical output passes", func(t *testing.T) {
		_, err := runVerifyTask(t, []string{"dist"},
			map[string]string{"dist/site.html": "site output\n"},
			map[string]string{"dist/site.html": "site output\n"})
		if err != nil {
			t.Fatalf("expected identical output to pass, got: %v", err)
		}
	})

	t.Run("changed file fails with diff", func(t *testing.T) {
		output, err := runVerifyTask(t, []string{"dist"},
			map[string]string{"dist/site.html": "site output\n"},
			map[string]string{"dist/site.html": "new site output\n"})
		if err == nil {
			t.Fatal("expected changed file to fail, got nil")
		}

		if !strings.Contains(output, "---") || !strings.Contains(output, "+++") {
			t.Errorf("expected diff in task output, got:\n%s", output)
		}
		if !strings.Contains(output, "-site output") || !strings.Contains(output, "+new site output") {
			t.Errorf("expected diff content in task output, got:\n%s", output)
		}
		expectedErr := "plugin \"mockplugin\" task \"generate\" outputs differ from checkout; run 'ap generate' to update"
		if !strings.Contains(err.Error(), expectedErr) {
			t.Errorf("expected error message directing to run ap generate, got: %v", err)
		}
	})

	t.Run("missing file fails", func(t *testing.T) {
		output, err := runVerifyTask(t, []string{"dist"},
			nil,
			map[string]string{"dist/missing.html": "staged content\n"})
		if err == nil {
			t.Fatal("expected missing file to fail, got nil")
		}

		if !strings.Contains(output, "missing.html") {
			t.Errorf("expected diff naming missing file in task output, got:\n%s", output)
		}
		expectedErr := "plugin \"mockplugin\" task \"generate\" outputs differ from checkout; run 'ap generate' to update"
		if !strings.Contains(err.Error(), expectedErr) {
			t.Errorf("expected error message directing to run ap generate, got: %v", err)
		}
	})

	t.Run("co-located mode ignores unrelated files", func(t *testing.T) {
		_, err := runVerifyTask(t, []string{"."},
			map[string]string{"unrelated.txt": "human authored\n", "gen.txt": "staged content\n"},
			map[string]string{"gen.txt": "staged content\n"})
		if err != nil {
			t.Fatalf("expected co-located mode to ignore unrelated file and pass, got: %v", err)
		}
	})
}

type hangingPluginServer struct {
	api.UnimplementedPluginServiceServer
}

func (s *hangingPluginServer) Describe(ctx context.Context, _ *api.DescribeRequest) (*api.DescribeResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestDescribeContainer_Deadline(t *testing.T) {
	repoRoot := t.TempDir()
	cacheDir := t.TempDir()
	tree, err := sourcetree.Open(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	scope := tasks.NewTaskContext(repoRoot, repoRoot, tree)

	hangingStarter := func(ctx context.Context, scope *tasks.TaskContext, imageRef string, allowNetwork bool, stderr io.Writer) (api.PluginServiceClient, io.Closer, error) {
		serverReader, clientWriter := io.Pipe()
		clientReader, serverWriter := io.Pipe()

		serverListener := stdio.NewListener(serverReader, serverWriter)
		clientConnection := stdio.NewConn(clientReader, clientWriter)

		grpcServer := grpc.NewServer()
		api.RegisterPluginServiceServer(grpcServer, &hangingPluginServer{})

		go func() {
			_ = grpcServer.Serve(serverListener)
		}()

		client, closer, dialError := plugins.DialStdioConn(ctx, clientConnection)
		if dialError != nil {
			_ = serverListener.Close()
			return nil, nil, dialError
		}
		wrappedCloser := closerFunc(func() error {
			grpcServer.Stop()
			return errors.Join(closer.Close(), serverListener.Close())
		})
		return client, wrappedCloser, nil
	}

	pluginName := "sleepy"
	imageRef := "sleepy-image:latest"

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	_, err = plugins.DescribeContainer(ctx, scope, cacheDir, pluginName, imageRef, hangingStarter)
	if err == nil {
		t.Fatal("expected DescribeContainer to fail due to deadline, got nil")
	}
	errorString := err.Error()
	if !strings.Contains(errorString, pluginName) {
		t.Errorf("expected error to contain plugin name %q, got: %s", pluginName, errorString)
	}
	if !strings.Contains(errorString, imageRef) {
		t.Errorf("expected error to contain image ref %q, got: %s", imageRef, errorString)
	}
	if !strings.Contains(errorString, "did not answer Describe within 30s") {
		t.Errorf("expected error to state deadline exceeded, got: %s", errorString)
	}
}
