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

package sdk

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins/api"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins/stdio"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type taskExecutionOutput struct {
	result      *api.TaskResult
	outputFiles []*api.FileChunk
	stdout      string
	stderr      string
}

func setupTestClient(t *testing.T, plugin Plugin) api.PluginServiceClient {
	t.Helper()
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()

	serverListener := stdio.NewListener(serverReader, serverWriter)
	clientConnection := stdio.NewConn(clientReader, clientWriter)

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- serve(serverListener, plugin)
	}()

	grpcClientConnection, err := grpc.NewClient("passthrough:///stdio",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return clientConnection, nil
		}),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}

	t.Cleanup(func() {
		_ = grpcClientConnection.Close()
		_ = serverListener.Close()
		_ = clientConnection.Close()
		if err := <-serverDone; err != nil {
			t.Errorf("serve error: %v", err)
		}
	})

	return api.NewPluginServiceClient(grpcClientConnection)
}

func executeTask(t *testing.T, client api.PluginServiceClient, start *api.TaskStart, files ...*api.FileChunk) (*taskExecutionOutput, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	stream, err := client.RunTask(ctx)
	if err != nil {
		return nil, fmt.Errorf("RunTask: %w", err)
	}

	if err := stream.Send(&api.RunTaskRequest{
		Payload: &api.RunTaskRequest_Start{Start: start},
	}); err != nil {
		return nil, fmt.Errorf("send TaskStart: %w", err)
	}

	for _, file := range files {
		if err := stream.Send(&api.RunTaskRequest{
			Payload: &api.RunTaskRequest_File{File: file},
		}); err != nil {
			return nil, fmt.Errorf("send file %s: %w", file.Path, err)
		}
	}
	if err := stream.CloseSend(); err != nil {
		return nil, fmt.Errorf("CloseSend: %w", err)
	}

	output := &taskExecutionOutput{}
	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if output.result != nil {
			t.Fatalf("received message after TaskResult: %+v", response)
		}

		switch payload := response.Payload.(type) {
		case *api.RunTaskResponse_Log:
			if len(output.outputFiles) > 0 {
				t.Errorf("log line received after output files")
			}
			if payload.Log.Stream == api.LogLine_STDOUT {
				output.stdout += string(payload.Log.Data)
			} else {
				output.stderr += string(payload.Log.Data)
			}
		case *api.RunTaskResponse_OutputFile:
			output.outputFiles = append(output.outputFiles, payload.OutputFile)
		case *api.RunTaskResponse_Result:
			output.result = payload.Result
		default:
			t.Fatalf("unexpected response payload: %+v", response)
		}
	}
	return output, nil
}

func TestDescribe(t *testing.T) {
	plugin := Plugin{
		Name:       "test-plugin",
		Activation: []string{"*.foo", "dir/*.bar"},
		Network:    true,
		Tasks: []Task{
			{
				Name:        "task1",
				Verb:        "build",
				Description: "Build task",
				Outputs:     []string{"out1.txt"},
			},
			{
				Name:        "task2",
				Verb:        "lint",
				Description: "Lint task",
				Outputs:     []string{"lint.json"},
			},
		},
	}
	client := setupTestClient(t, plugin)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	response, err := client.Describe(ctx, &api.DescribeRequest{})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if response.Name != "test-plugin" {
		t.Errorf("expected name 'test-plugin', got %q", response.Name)
	}
	if !response.Network {
		t.Errorf("expected network true, got false")
	}
	expectedActivation := []string{"*.foo", "dir/*.bar"}
	if !reflect.DeepEqual(response.Activation, expectedActivation) {
		t.Errorf("expected activation %v, got %v", expectedActivation, response.Activation)
	}
	if len(response.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(response.Tasks))
	}
	if response.Tasks[0].Name != "task1" || response.Tasks[0].Verb != "build" || response.Tasks[0].Description != "Build task" {
		t.Errorf("unexpected task 0: %+v", response.Tasks[0])
	}
	if !reflect.DeepEqual(response.Tasks[0].Outputs, []string{"out1.txt"}) {
		t.Errorf("unexpected task 0 outputs: %v", response.Tasks[0].Outputs)
	}
	if response.Tasks[1].Name != "task2" || response.Tasks[1].Verb != "lint" || response.Tasks[1].Description != "Lint task" {
		t.Errorf("unexpected task 1: %+v", response.Tasks[1])
	}
}

func TestRunTask_Success(t *testing.T) {
	taskExecuted := false
	plugin := Plugin{
		Name: "test-plugin",
		Tasks: []Task{
			{
				Name: "build",
				Verb: "build",
				Run: func(ctx context.Context, task TaskContext) error {
					taskExecuted = true
					inputData, err := os.ReadFile(filepath.Join(task.Dir, "hello.txt"))
					if err != nil {
						return fmt.Errorf("reading hello.txt: %w", err)
					}
					if string(inputData) != "world" {
						return fmt.Errorf("unexpected hello.txt content: %q", string(inputData))
					}
					rootData, err := os.ReadFile(filepath.Join(task.RootDir, "root.txt"))
					if err != nil {
						return fmt.Errorf("reading root.txt: %w", err)
					}
					if string(rootData) != "root-file" {
						return fmt.Errorf("unexpected root.txt content: %q", string(rootData))
					}
					rootInfo, err := os.Stat(filepath.Join(task.RootDir, "root.txt"))
					if err != nil {
						return fmt.Errorf("stat root.txt: %w", err)
					}
					if rootInfo.Mode().Perm() != 0755 {
						return fmt.Errorf("expected root.txt mode 0755, got 0%o", rootInfo.Mode().Perm())
					}

					if _, err := fmt.Fprint(task.Stdout, "building output\n"); err != nil {
						return err
					}
					if _, err := fmt.Fprint(task.Stderr, "diagnostics line\n"); err != nil {
						return err
					}

					outputPath := filepath.Join(task.OutputDir, "sub", "output.txt")
					if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
						return err
					}
					return os.WriteFile(outputPath, []byte("result content"), 0755)
				},
			},
		},
	}

	client := setupTestClient(t, plugin)
	start := &api.TaskStart{Task: "build", Dir: "activated"}
	helloFile := &api.FileChunk{Path: "activated/hello.txt", Mode: 0644, Content: []byte("world"), IsLastChunk: true}
	rootFile := &api.FileChunk{Path: "root.txt", Mode: 0755, Content: []byte("root-file"), IsLastChunk: true}

	output, err := executeTask(t, client, start, helloFile, rootFile)
	if err != nil {
		t.Fatalf("executeTask: %v", err)
	}

	if !taskExecuted {
		t.Fatalf("task was not executed")
	}
	if output.stdout != "building output\n" {
		t.Errorf("expected stdout 'building output\\n', got %q", output.stdout)
	}
	if output.stderr != "diagnostics line\n" {
		t.Errorf("expected stderr 'diagnostics line\\n', got %q", output.stderr)
	}
	if len(output.outputFiles) != 1 {
		t.Fatalf("expected 1 output file, got %d", len(output.outputFiles))
	}
	if output.outputFiles[0].Path != "sub/output.txt" {
		t.Errorf("expected output path 'sub/output.txt', got %q", output.outputFiles[0].Path)
	}
	if string(output.outputFiles[0].Content) != "result content" {
		t.Errorf("expected content 'result content', got %q", string(output.outputFiles[0].Content))
	}
	if output.outputFiles[0].Mode != 0755 {
		t.Errorf("expected mode 0755, got 0%o", output.outputFiles[0].Mode)
	}
	if !output.outputFiles[0].IsLastChunk {
		t.Errorf("expected IsLastChunk true")
	}
	if output.result == nil {
		t.Fatalf("missing TaskResult")
	}
	if output.result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", output.result.ExitCode)
	}
}

func TestRunTask_Failure(t *testing.T) {
	plugin := Plugin{
		Name: "test-plugin",
		Tasks: []Task{
			{
				Name: "fail-task",
				Run: func(ctx context.Context, task TaskContext) error {
					return errors.New("task execution failed")
				},
			},
		},
	}

	client := setupTestClient(t, plugin)
	output, err := executeTask(t, client, &api.TaskStart{Task: "fail-task"})
	if err != nil {
		t.Fatalf("executeTask: %v", err)
	}
	if output.result == nil {
		t.Fatalf("expected TaskResult")
	}
	if output.result.ExitCode == 0 {
		t.Errorf("expected non-zero exit code")
	}
	if output.result.ErrorMessage != "task execution failed" {
		t.Errorf("unexpected error message: %q", output.result.ErrorMessage)
	}
}

func TestRunTask_PathEscapeRejected(t *testing.T) {
	plugin := Plugin{
		Name: "test-plugin",
		Tasks: []Task{
			{
				Name: "task",
				Run: func(ctx context.Context, task TaskContext) error {
					return nil
				},
			},
		},
	}

	client := setupTestClient(t, plugin)
	escapeFile := &api.FileChunk{Path: "../outside.txt", Mode: 0644, Content: []byte("escape"), IsLastChunk: true}
	_, err := executeTask(t, client, &api.TaskStart{Task: "task"}, escapeFile)
	if err == nil {
		t.Fatalf("expected error for escaping path, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got code %v: %v", status.Code(err), err)
	}
}

func TestExamplePlugin(t *testing.T) {
	temporaryDirectory := t.TempDir()
	binaryPath := filepath.Join(temporaryDirectory, "example-plugin")
	buildCommand := exec.Command("go", "build", "-buildvcs=false", "-o", binaryPath, "./example")
	buildCommand.Env = os.Environ()
	buildOutput, err := buildCommand.CombinedOutput()
	if err != nil {
		t.Fatalf("building example plugin: %v\n%s", err, string(buildOutput))
	}

	command := exec.Command(binaryPath)
	stdinPipe, err := command.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdoutPipe, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("command.Start: %v", err)
	}
	connection := stdio.NewCommandConn(command, stdinPipe, stdoutPipe)
	defer func() { _ = connection.Close() }()

	grpcClientConnection, err := grpc.NewClient("passthrough:///stdio",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return connection, nil
		}),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer func() { _ = grpcClientConnection.Close() }()

	client := api.NewPluginServiceClient(grpcClientConnection)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	describeResponse, err := client.Describe(ctx, &api.DescribeRequest{})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if describeResponse.Name != "example" {
		t.Errorf("expected name 'example', got %q", describeResponse.Name)
	}

	inputFile := &api.FileChunk{
		Path:        "docs/input.txt",
		Mode:        0644,
		Content:     []byte("hello world\n"),
		IsLastChunk: true,
	}
	output, err := executeTask(t, client, &api.TaskStart{Task: "append", Dir: "docs"}, inputFile)
	if err != nil {
		t.Fatalf("executeTask: %v", err)
	}
	if output.result == nil || output.result.ExitCode != 0 {
		t.Fatalf("task failed: %+v", output.result)
	}
	if output.stdout != "processed input.txt\n" {
		t.Errorf("expected log 'processed input.txt\\n', got %q", output.stdout)
	}
	if len(output.outputFiles) != 1 {
		t.Fatalf("expected 1 output file, got %d", len(output.outputFiles))
	}
	expectedContent := "hello world\nappended line\n"
	if string(output.outputFiles[0].Content) != expectedContent {
		t.Errorf("expected output %q, got %q", expectedContent, string(output.outputFiles[0].Content))
	}
}

func TestRunTask_MultiChunkFiles(t *testing.T) {
	largeSize := int(1.5 * 1024 * 1024)
	largeData := make([]byte, largeSize)
	for index := range largeData {
		largeData[index] = byte(index % 251)
	}

	plugin := Plugin{
		Name: "test-plugin",
		Tasks: []Task{
			{
				Name: "chunk-task",
				Run: func(ctx context.Context, task TaskContext) error {
					readData, err := os.ReadFile(filepath.Join(task.Dir, "multi-chunk.bin"))
					if err != nil {
						return fmt.Errorf("reading multi-chunk.bin: %w", err)
					}
					if !bytes.Equal(readData, []byte("chunk1-chunk2")) {
						return fmt.Errorf("unexpected content: %q", string(readData))
					}

					if err := os.WriteFile(filepath.Join(task.OutputDir, "large.bin"), largeData, 0644); err != nil {
						return err
					}
					return os.WriteFile(filepath.Join(task.OutputDir, "empty.txt"), nil, 0644)
				},
			},
		},
	}

	client := setupTestClient(t, plugin)
	chunk1 := &api.FileChunk{Path: "sub/multi-chunk.bin", Mode: 0644, Content: []byte("chunk1-"), IsLastChunk: false}
	chunk2 := &api.FileChunk{Path: "sub/multi-chunk.bin", Mode: 0644, Content: []byte("chunk2"), IsLastChunk: true}

	output, err := executeTask(t, client, &api.TaskStart{Task: "chunk-task", Dir: "sub"}, chunk1, chunk2)
	if err != nil {
		t.Fatalf("executeTask: %v", err)
	}

	if output.result == nil || output.result.ExitCode != 0 {
		t.Fatalf("task failed: %+v", output.result)
	}
	if len(output.outputFiles) != 3 {
		t.Fatalf("expected 3 output chunks, got %d", len(output.outputFiles))
	}
	if output.outputFiles[0].Path != "empty.txt" || len(output.outputFiles[0].Content) != 0 || !output.outputFiles[0].IsLastChunk {
		t.Errorf("unexpected empty.txt chunk: %+v", output.outputFiles[0])
	}
	if output.outputFiles[1].Path != "large.bin" || len(output.outputFiles[1].Content) != maxChunkSize || output.outputFiles[1].IsLastChunk {
		t.Errorf("unexpected large.bin chunk 1: len=%d, isLast=%v", len(output.outputFiles[1].Content), output.outputFiles[1].IsLastChunk)
	}
	if output.outputFiles[2].Path != "large.bin" || len(output.outputFiles[2].Content) != largeSize-maxChunkSize || !output.outputFiles[2].IsLastChunk {
		t.Errorf("unexpected large.bin chunk 2: len=%d, isLast=%v", len(output.outputFiles[2].Content), output.outputFiles[2].IsLastChunk)
	}
	receivedLarge := append(output.outputFiles[1].Content, output.outputFiles[2].Content...)
	if !bytes.Equal(receivedLarge, largeData) {
		t.Errorf("reconstructed large.bin does not match original data")
	}
}
