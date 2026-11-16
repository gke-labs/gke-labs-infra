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

func setupTestClient(t *testing.T, plugin Plugin) (api.PluginServiceClient, func()) {
	t.Helper()
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()

	serverListener := stdio.NewListener(serverReader, serverWriter)
	clientConn := stdio.NewConn(clientReader, clientWriter)

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- Serve(serverListener, plugin)
	}()

	grpcClientConn, err := grpc.NewClient("passthrough:///stdio",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return clientConn, nil
		}),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}

	cleanup := func() {
		_ = grpcClientConn.Close()
		_ = serverListener.Close()
		_ = clientConn.Close()
		if err := <-serverDone; err != nil {
			t.Errorf("Serve error: %v", err)
		}
	}

	return api.NewPluginServiceClient(grpcClientConn), cleanup
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
	client, cleanup := setupTestClient(t, plugin)
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	resp, err := client.Describe(ctx, &api.DescribeRequest{})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if resp.Name != "test-plugin" {
		t.Errorf("expected name 'test-plugin', got %q", resp.Name)
	}
	if !resp.Network {
		t.Errorf("expected network true, got false")
	}
	expectedActivation := []string{"*.foo", "dir/*.bar"}
	if !reflect.DeepEqual(resp.Activation, expectedActivation) {
		t.Errorf("expected activation %v, got %v", expectedActivation, resp.Activation)
	}
	if len(resp.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(resp.Tasks))
	}
	if resp.Tasks[0].Name != "task1" || resp.Tasks[0].Verb != "build" || resp.Tasks[0].Description != "Build task" {
		t.Errorf("unexpected task 0: %+v", resp.Tasks[0])
	}
	if !reflect.DeepEqual(resp.Tasks[0].Outputs, []string{"out1.txt"}) {
		t.Errorf("unexpected task 0 outputs: %v", resp.Tasks[0].Outputs)
	}
	if resp.Tasks[1].Name != "task2" || resp.Tasks[1].Verb != "lint" || resp.Tasks[1].Description != "Lint task" {
		t.Errorf("unexpected task 1: %+v", resp.Tasks[1])
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
					// Verify input file in activated dir
					inputData, err := os.ReadFile(filepath.Join(task.Dir, "hello.txt"))
					if err != nil {
						return fmt.Errorf("reading hello.txt: %w", err)
					}
					if string(inputData) != "world" {
						return fmt.Errorf("unexpected hello.txt content: %q", string(inputData))
					}
					// Verify input file in root dir
					rootData, err := os.ReadFile(filepath.Join(task.RootDir, "root.txt"))
					if err != nil {
						return fmt.Errorf("reading root.txt: %w", err)
					}
					if string(rootData) != "root-file" {
						return fmt.Errorf("unexpected root.txt content: %q", string(rootData))
					}

					// Stream logs
					if _, err := fmt.Fprint(task.Stdout, "building output\n"); err != nil {
						return err
					}
					if _, err := fmt.Fprint(task.Stderr, "diagnostics line\n"); err != nil {
						return err
					}

					// Generate output file
					outputPath := filepath.Join(task.OutputDir, "sub", "output.txt")
					if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
						return err
					}
					return os.WriteFile(outputPath, []byte("result content"), 0755)
				},
			},
		},
	}

	client, cleanup := setupTestClient(t, plugin)
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	stream, err := client.RunTask(ctx)
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	// 1. Send TaskStart
	if err := stream.Send(&api.RunTaskRequest{
		Payload: &api.RunTaskRequest_Start{
			Start: &api.TaskStart{
				Task: "build",
				Dir:  "activated",
			},
		},
	}); err != nil {
		t.Fatalf("sending TaskStart: %v", err)
	}

	// 2. Stream input files
	if err := stream.Send(&api.RunTaskRequest{
		Payload: &api.RunTaskRequest_File{
			File: &api.FileChunk{
				Path:        "activated/hello.txt",
				Mode:        0644,
				Content:     []byte("world"),
				IsLastChunk: true,
			},
		},
	}); err != nil {
		t.Fatalf("sending hello.txt: %v", err)
	}
	if err := stream.Send(&api.RunTaskRequest{
		Payload: &api.RunTaskRequest_File{
			File: &api.FileChunk{
				Path:        "root.txt",
				Mode:        0644,
				Content:     []byte("root-file"),
				IsLastChunk: true,
			},
		},
	}); err != nil {
		t.Fatalf("sending root.txt: %v", err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}

	// 3. Receive responses and verify ordering
	var logStdout string
	var logStderr string
	var outputFiles []*api.FileChunk
	var result *api.TaskResult
	seenResult := false

	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("stream.Recv: %v", err)
		}
		if seenResult {
			t.Fatalf("received message after TaskResult: %+v", resp)
		}

		switch payload := resp.Payload.(type) {
		case *api.RunTaskResponse_Log:
			if len(outputFiles) > 0 {
				t.Errorf("log line received after output files")
			}
			if payload.Log.Stream == api.LogLine_STDOUT {
				logStdout += string(payload.Log.Data)
			} else {
				logStderr += string(payload.Log.Data)
			}
		case *api.RunTaskResponse_OutputFile:
			outputFiles = append(outputFiles, payload.OutputFile)
		case *api.RunTaskResponse_Result:
			result = payload.Result
			seenResult = true
		default:
			t.Fatalf("unexpected response payload: %+v", resp)
		}
	}

	if !taskExecuted {
		t.Fatalf("task was not executed")
	}
	if logStdout != "building output\n" {
		t.Errorf("expected stdout 'building output\\n', got %q", logStdout)
	}
	if logStderr != "diagnostics line\n" {
		t.Errorf("expected stderr 'diagnostics line\\n', got %q", logStderr)
	}
	if len(outputFiles) != 1 {
		t.Fatalf("expected 1 output file, got %d", len(outputFiles))
	}
	if outputFiles[0].Path != "sub/output.txt" {
		t.Errorf("expected output path 'sub/output.txt', got %q", outputFiles[0].Path)
	}
	if string(outputFiles[0].Content) != "result content" {
		t.Errorf("expected content 'result content', got %q", string(outputFiles[0].Content))
	}
	if outputFiles[0].Mode != 0755 {
		t.Errorf("expected mode 0755, got 0%o", outputFiles[0].Mode)
	}
	if !outputFiles[0].IsLastChunk {
		t.Errorf("expected IsLastChunk true")
	}
	if result == nil {
		t.Fatalf("missing TaskResult")
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
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

	client, cleanup := setupTestClient(t, plugin)
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	stream, err := client.RunTask(ctx)
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	if err := stream.Send(&api.RunTaskRequest{
		Payload: &api.RunTaskRequest_Start{
			Start: &api.TaskStart{Task: "fail-task"},
		},
	}); err != nil {
		t.Fatalf("sending TaskStart: %v", err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}

	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("stream.Recv: %v", err)
	}
	result := resp.GetResult()
	if result == nil {
		t.Fatalf("expected TaskResult, got %+v", resp)
	}
	if result.ExitCode == 0 {
		t.Errorf("expected non-zero exit code")
	}
	if result.ErrorMessage != "task execution failed" {
		t.Errorf("unexpected error message: %q", result.ErrorMessage)
	}

	// Verify stream ends cleanly
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Errorf("expected io.EOF after TaskResult, got %v", err)
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

	client, cleanup := setupTestClient(t, plugin)
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	stream, err := client.RunTask(ctx)
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	if err := stream.Send(&api.RunTaskRequest{
		Payload: &api.RunTaskRequest_Start{
			Start: &api.TaskStart{Task: "task"},
		},
	}); err != nil {
		t.Fatalf("sending TaskStart: %v", err)
	}

	if err := stream.Send(&api.RunTaskRequest{
		Payload: &api.RunTaskRequest_File{
			File: &api.FileChunk{
				Path:        "../outside.txt",
				Content:     []byte("escape"),
				IsLastChunk: true,
			},
		},
	}); err != nil {
		t.Fatalf("sending file: %v", err)
	}
	_ = stream.CloseSend()

	_, err = stream.Recv()
	if err == nil {
		t.Fatalf("expected error for escaping path, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got code %v: %v", status.Code(err), err)
	}
}

func TestExamplePlugin(t *testing.T) {
	tempDir := t.TempDir()
	binPath := filepath.Join(tempDir, "example-plugin")
	buildCmd := exec.Command("go", "build", "-o", binPath, "./example")
	buildCmd.Env = os.Environ()
	out, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building example plugin: %v\n%s", err, string(out))
	}

	cmd := exec.Command(binPath)
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start: %v", err)
	}
	conn := stdio.NewCommandConn(cmd, stdinPipe, stdoutPipe)
	defer func() { _ = conn.Close() }()

	grpcClientConn, err := grpc.NewClient("passthrough:///stdio",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return conn, nil
		}),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer func() { _ = grpcClientConn.Close() }()

	client := api.NewPluginServiceClient(grpcClientConn)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// 1. Verify Describe
	desc, err := client.Describe(ctx, &api.DescribeRequest{})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.Name != "example" {
		t.Errorf("expected name 'example', got %q", desc.Name)
	}

	// 2. Verify RunTask
	stream, err := client.RunTask(ctx)
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	if err := stream.Send(&api.RunTaskRequest{
		Payload: &api.RunTaskRequest_Start{
			Start: &api.TaskStart{
				Task: "append",
				Dir:  "docs",
			},
		},
	}); err != nil {
		t.Fatalf("send TaskStart: %v", err)
	}

	if err := stream.Send(&api.RunTaskRequest{
		Payload: &api.RunTaskRequest_File{
			File: &api.FileChunk{
				Path:        "docs/input.txt",
				Content:     []byte("hello world\n"),
				IsLastChunk: true,
			},
		},
	}); err != nil {
		t.Fatalf("send FileChunk: %v", err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}

	var outputContent string
	var logOutput string
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if log := resp.GetLog(); log != nil {
			logOutput += string(log.Data)
		}
		if file := resp.GetOutputFile(); file != nil {
			outputContent += string(file.Content)
		}
		if result := resp.GetResult(); result != nil {
			if result.ExitCode != 0 {
				t.Fatalf("task failed with code %d: %s", result.ExitCode, result.ErrorMessage)
			}
		}
	}

	if logOutput != "processed input.txt\n" {
		t.Errorf("expected log 'processed input.txt\\n', got %q", logOutput)
	}
	expectedContent := "hello world\nappended line\n"
	if outputContent != expectedContent {
		t.Errorf("expected output %q, got %q", expectedContent, outputContent)
	}
}

func TestRunTask_MultiChunkFiles(t *testing.T) {
	largeSize := int(1.5 * 1024 * 1024)
	largeData := make([]byte, largeSize)
	for i := range largeData {
		largeData[i] = byte(i % 251)
	}

	plugin := Plugin{
		Name: "test-plugin",
		Tasks: []Task{
			{
				Name: "chunk-task",
				Run: func(ctx context.Context, task TaskContext) error {
					// Verify multi-chunk input file
					readData, err := os.ReadFile(filepath.Join(task.Dir, "multi-chunk.bin"))
					if err != nil {
						return fmt.Errorf("reading multi-chunk.bin: %w", err)
					}
					if !bytes.Equal(readData, []byte("chunk1-chunk2")) {
						return fmt.Errorf("unexpected content: %q", string(readData))
					}

					// Create large output file (1.5 MB)
					if err := os.WriteFile(filepath.Join(task.OutputDir, "large.bin"), largeData, 0644); err != nil {
						return err
					}
					// Create empty output file (0 bytes)
					return os.WriteFile(filepath.Join(task.OutputDir, "empty.txt"), nil, 0644)
				},
			},
		},
	}

	client, cleanup := setupTestClient(t, plugin)
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	stream, err := client.RunTask(ctx)
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	if err := stream.Send(&api.RunTaskRequest{
		Payload: &api.RunTaskRequest_Start{
			Start: &api.TaskStart{
				Task: "chunk-task",
				Dir:  "sub",
			},
		},
	}); err != nil {
		t.Fatalf("send TaskStart: %v", err)
	}

	// Send multi-chunk input file: chunk 1
	if err := stream.Send(&api.RunTaskRequest{
		Payload: &api.RunTaskRequest_File{
			File: &api.FileChunk{
				Path:        "sub/multi-chunk.bin",
				Content:     []byte("chunk1-"),
				IsLastChunk: false,
			},
		},
	}); err != nil {
		t.Fatalf("send FileChunk 1: %v", err)
	}
	// chunk 2
	if err := stream.Send(&api.RunTaskRequest{
		Payload: &api.RunTaskRequest_File{
			File: &api.FileChunk{
				Path:        "sub/multi-chunk.bin",
				Content:     []byte("chunk2"),
				IsLastChunk: true,
			},
		},
	}); err != nil {
		t.Fatalf("send FileChunk 2: %v", err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}

	var outputChunks []*api.FileChunk
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if file := resp.GetOutputFile(); file != nil {
			outputChunks = append(outputChunks, file)
		}
		if res := resp.GetResult(); res != nil {
			if res.ExitCode != 0 {
				t.Fatalf("task failed with %d: %s", res.ExitCode, res.ErrorMessage)
			}
		}
	}

	// Expect 3 chunks: empty.txt (1 chunk), large.bin (2 chunks: 1MB not last, 0.5MB is last)
	if len(outputChunks) != 3 {
		t.Fatalf("expected 3 output chunks, got %d", len(outputChunks))
	}
	if outputChunks[0].Path != "empty.txt" || len(outputChunks[0].Content) != 0 || !outputChunks[0].IsLastChunk {
		t.Errorf("unexpected empty.txt chunk: %+v", outputChunks[0])
	}
	if outputChunks[1].Path != "large.bin" || len(outputChunks[1].Content) != maxChunkSize || outputChunks[1].IsLastChunk {
		t.Errorf("unexpected large.bin chunk 1: len=%d, isLast=%v", len(outputChunks[1].Content), outputChunks[1].IsLastChunk)
	}
	if outputChunks[2].Path != "large.bin" || len(outputChunks[2].Content) != largeSize-maxChunkSize || !outputChunks[2].IsLastChunk {
		t.Errorf("unexpected large.bin chunk 2: len=%d, isLast=%v", len(outputChunks[2].Content), outputChunks[2].IsLastChunk)
	}
	receivedLarge := append(outputChunks[1].Content, outputChunks[2].Content...)
	if !bytes.Equal(receivedLarge, largeData) {
		t.Errorf("reconstructed large.bin does not match original data")
	}
}
