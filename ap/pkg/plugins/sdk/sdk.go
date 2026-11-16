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
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins/api"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/plugins/stdio"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const maxChunkSize = 1024 * 1024 // 1 MB

// TaskContext provides the filesystem locations and log writers for a task.
type TaskContext struct {
	// RootDir is the temporary directory containing the streamed repository files.
	RootDir string

	// Dir is the activated directory within RootDir where the task executes.
	Dir string

	// OutputDir is the temporary directory where the task writes its generated files.
	OutputDir string

	// Stdout receives standard output log lines to stream back to ap.
	Stdout io.Writer

	// Stderr receives standard error log lines to stream back to ap.
	Stderr io.Writer
}

// TaskFunc is the function signature for a task implementation.
type TaskFunc func(ctx context.Context, task TaskContext) error

// Task describes a single task supported by a plugin.
type Task struct {
	// Name is the task identifier (e.g. "build").
	Name string

	// Verb is the ap verb this task maps to (e.g. "build", "generate", "test", "lint").
	Verb string

	// Description is a human-readable explanation of what the task does.
	Description string

	// Outputs lists output files or directory patterns produced by the task, relative to the activated directory.
	Outputs []string

	// Run is the function that executes the task logic.
	Run TaskFunc
}

// Plugin describes a plugin and its capabilities.
type Plugin struct {
	// Name is the identifier of the plugin (e.g. "astro").
	Name string

	// Activation holds repository-relative glob patterns that trigger this plugin.
	Activation []string

	// Network indicates whether the plugin requires network access during task execution.
	Network bool

	// Tasks lists the tasks provided by this plugin.
	Tasks []Task
}

// ServeStdio serves the plugin over stdin and stdout using the gRPC stdio transport.
func ServeStdio(plugin Plugin) error {
	listener := stdio.NewListener(os.Stdin, os.Stdout)
	return Serve(listener, plugin)
}

// Serve serves the plugin over the given net.Listener using the gRPC transport until closed.
func Serve(listener net.Listener, plugin Plugin) error {
	grpcServer := grpc.NewServer()
	api.RegisterPluginServiceServer(grpcServer, newServer(plugin))
	if err := grpcServer.Serve(listener); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

type server struct {
	api.UnimplementedPluginServiceServer
	plugin Plugin
	tasks  map[string]Task
}

func newServer(plugin Plugin) *server {
	taskMap := make(map[string]Task, len(plugin.Tasks))
	for _, task := range plugin.Tasks {
		taskMap[task.Name] = task
	}
	return &server{
		plugin: plugin,
		tasks:  taskMap,
	}
}

func (pluginServer *server) Describe(_ context.Context, _ *api.DescribeRequest) (*api.DescribeResponse, error) {
	var tasksMetadata []*api.TaskMetadata
	for _, task := range pluginServer.plugin.Tasks {
		tasksMetadata = append(tasksMetadata, &api.TaskMetadata{
			Name:        task.Name,
			Verb:        task.Verb,
			Description: task.Description,
			Outputs:     task.Outputs,
		})
	}
	return &api.DescribeResponse{
		Name:       pluginServer.plugin.Name,
		Activation: pluginServer.plugin.Activation,
		Network:    pluginServer.plugin.Network,
		Tasks:      tasksMetadata,
	}, nil
}

func (pluginServer *server) RunTask(stream api.PluginService_RunTaskServer) error {
	firstRequest, err := stream.Recv()
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "receiving task start: %v", err)
	}
	start := firstRequest.GetStart()
	if start == nil {
		return status.Errorf(codes.InvalidArgument, "first message must be TaskStart")
	}

	task, ok := pluginServer.tasks[start.Task]
	if !ok {
		return status.Errorf(codes.NotFound, "unknown task %q", start.Task)
	}

	if isPathEscape(start.Dir) {
		return status.Errorf(codes.InvalidArgument, "task directory %q escapes repository", start.Dir)
	}

	rootDir, err := os.MkdirTemp("", "ap-plugin-input-*")
	if err != nil {
		return status.Errorf(codes.Internal, "creating input directory: %v", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(rootDir); removeErr != nil && !os.IsNotExist(removeErr) {
			fmt.Fprintf(os.Stderr, "failed to remove %s: %v\n", rootDir, removeErr)
		}
	}()

	outputDir, err := os.MkdirTemp("", "ap-plugin-output-*")
	if err != nil {
		return status.Errorf(codes.Internal, "creating output directory: %v", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(outputDir); removeErr != nil && !os.IsNotExist(removeErr) {
			fmt.Fprintf(os.Stderr, "failed to remove %s: %v\n", outputDir, removeErr)
		}
	}()

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return status.Errorf(codes.Internal, "opening input root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "failed to close root: %v\n", closeErr)
		}
	}()

	for {
		request, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return status.Errorf(codes.Internal, "receiving file chunk: %v", err)
		}
		chunk := request.GetFile()
		if chunk == nil {
			return status.Errorf(codes.InvalidArgument, "unexpected message, expected FileChunk")
		}
		if isPathEscape(chunk.Path) {
			return status.Errorf(codes.InvalidArgument, "file path %q escapes directory", chunk.Path)
		}
		cleanPath := filepath.Clean(filepath.FromSlash(chunk.Path))
		if cleanPath == "." || cleanPath == "" {
			return status.Errorf(codes.InvalidArgument, "invalid file path %q", chunk.Path)
		}

		parentDir := filepath.Dir(cleanPath)
		if parentDir != "." {
			if err := root.MkdirAll(parentDir, 0755); err != nil {
				return status.Errorf(codes.Internal, "creating directory %q: %v", parentDir, err)
			}
		}

		fileInfo, err := root.Lstat(cleanPath)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return status.Errorf(codes.Internal, "stat file %q: %v", chunk.Path, err)
		}
		if err == nil && !fileInfo.Mode().IsRegular() {
			return status.Errorf(codes.InvalidArgument, "file path %q is not a regular file", chunk.Path)
		}

		mode := os.FileMode(chunk.Mode).Perm()

		file, err := root.OpenFile(cleanPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, mode)
		if err != nil {
			return status.Errorf(codes.Internal, "opening file %q: %v", chunk.Path, err)
		}
		if len(chunk.Content) > 0 {
			if _, err := file.Write(chunk.Content); err != nil {
				return status.Errorf(codes.Internal, "writing file %q: %v", chunk.Path, errors.Join(err, file.Close()))
			}
		}
		if err := file.Close(); err != nil {
			return status.Errorf(codes.Internal, "closing file %q: %v", chunk.Path, err)
		}
	}

	cleanDir := filepath.Clean(filepath.FromSlash(start.Dir))
	var activatedDir string
	if cleanDir == "." || cleanDir == "" {
		activatedDir = rootDir
	} else {
		activatedDir = filepath.Join(rootDir, cleanDir)
	}
	if err := os.MkdirAll(activatedDir, 0755); err != nil {
		return status.Errorf(codes.Internal, "creating activated directory: %v", err)
	}

	var sendMutex sync.Mutex
	sendResponse := func(response *api.RunTaskResponse) error {
		sendMutex.Lock()
		defer sendMutex.Unlock()
		return stream.Send(response)
	}

	stdoutWriter := &logWriter{streamType: api.LogLine_STDOUT, send: sendResponse}
	stderrWriter := &logWriter{streamType: api.LogLine_STDERR, send: sendResponse}

	taskContext := TaskContext{
		RootDir:   rootDir,
		Dir:       activatedDir,
		OutputDir: outputDir,
		Stdout:    stdoutWriter,
		Stderr:    stderrWriter,
	}

	taskError := task.Run(stream.Context(), taskContext)
	if taskError != nil {
		exitCode := int32(1)
		var exitError *exec.ExitError
		if errors.As(taskError, &exitError) && exitError.ExitCode() != 0 {
			exitCode = int32(exitError.ExitCode())
		}
		return sendResponse(&api.RunTaskResponse{
			Payload: &api.RunTaskResponse_Result{
				Result: &api.TaskResult{
					ExitCode:     exitCode,
					ErrorMessage: taskError.Error(),
				},
			},
		})
	}

	walkError := filepath.WalkDir(outputDir, func(path string, dirEntry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if dirEntry.IsDir() {
			return nil
		}
		fileInfo, err := dirEntry.Info()
		if err != nil {
			return err
		}
		if !fileInfo.Mode().IsRegular() {
			return nil
		}
		relativePath, err := filepath.Rel(outputDir, path)
		if err != nil {
			return err
		}
		slashPath := filepath.ToSlash(relativePath)
		return sendOutputFile(path, slashPath, uint32(fileInfo.Mode().Perm()), sendResponse)
	})
	if walkError != nil {
		return status.Errorf(codes.Internal, "streaming output files: %v", walkError)
	}

	return sendResponse(&api.RunTaskResponse{
		Payload: &api.RunTaskResponse_Result{
			Result: &api.TaskResult{
				ExitCode: 0,
			},
		},
	})
}

type logWriter struct {
	streamType api.LogLine_Stream
	send       func(*api.RunTaskResponse) error
}

func (writer *logWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	err := writer.send(&api.RunTaskResponse{
		Payload: &api.RunTaskResponse_Log{
			Log: &api.LogLine{
				Stream: writer.streamType,
				Data:   bytes.Clone(data),
			},
		},
	})
	if err != nil {
		return 0, err
	}
	return len(data), nil
}

func sendOutputFile(localPath, relativePath string, mode uint32, send func(*api.RunTaskResponse) error) error {
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "failed to close %s: %v\n", localPath, closeErr)
		}
	}()

	buffer := make([]byte, maxChunkSize)
	bytesRead, err := io.ReadFull(file, buffer)
	if errors.Is(err, io.EOF) {
		return send(&api.RunTaskResponse{
			Payload: &api.RunTaskResponse_OutputFile{
				OutputFile: &api.FileChunk{
					Path:        relativePath,
					Mode:        mode,
					Content:     nil,
					IsLastChunk: true,
				},
			},
		})
	}
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}

	currentContent := bytes.Clone(buffer[:bytesRead])
	nextBuffer := make([]byte, maxChunkSize)

	for {
		nextBytesRead, nextErr := io.ReadFull(file, nextBuffer)
		if errors.Is(nextErr, io.EOF) {
			return send(&api.RunTaskResponse{
				Payload: &api.RunTaskResponse_OutputFile{
					OutputFile: &api.FileChunk{
						Path:        relativePath,
						Mode:        mode,
						Content:     currentContent,
						IsLastChunk: true,
					},
				},
			})
		}
		if nextErr != nil && !errors.Is(nextErr, io.ErrUnexpectedEOF) {
			return nextErr
		}

		if err := send(&api.RunTaskResponse{
			Payload: &api.RunTaskResponse_OutputFile{
				OutputFile: &api.FileChunk{
					Path:        relativePath,
					Mode:        mode,
					Content:     currentContent,
					IsLastChunk: false,
				},
			},
		}); err != nil {
			return err
		}

		currentContent = bytes.Clone(nextBuffer[:nextBytesRead])
	}
}

func isPathEscape(path string) bool {
	if filepath.IsAbs(path) || strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\") {
		return true
	}
	cleanPath := filepath.Clean(filepath.FromSlash(path))
	return cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) || strings.HasPrefix(cleanPath, "../")
}
