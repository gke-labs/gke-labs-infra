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
	RootDir   string
	Dir       string
	OutputDir string
	Stdout    io.Writer
	Stderr    io.Writer
}

// TaskFunc is the function signature for a task implementation.
type TaskFunc func(ctx context.Context, task TaskContext) error

// Task describes a single task supported by a plugin.
type Task struct {
	Name        string
	Verb        string
	Description string
	Outputs     []string
	Run         TaskFunc
}

// Plugin describes a plugin and its capabilities.
type Plugin struct {
	Name       string
	Activation []string
	Network    bool
	Tasks      []Task
}

// ServeStdio serves the plugin over stdin and stdout using the gRPC stdio transport.
func ServeStdio(plugin Plugin) error {
	listener := stdio.NewListener(os.Stdin, os.Stdout)
	if err := Serve(listener, plugin); err != nil {
		fmt.Fprintf(os.Stderr, "plugin %s: %v\n", plugin.Name, err)
		return err
	}
	return nil
}

// Serve serves the plugin over the provided net.Listener.
func Serve(listener net.Listener, plugin Plugin) error {
	server := grpc.NewServer()
	api.RegisterPluginServiceServer(server, newServer(plugin))
	if err := server.Serve(listener); err != nil && !errors.Is(err, net.ErrClosed) {
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

func (s *server) Describe(_ context.Context, _ *api.DescribeRequest) (*api.DescribeResponse, error) {
	var taskMeta []*api.TaskMetadata
	for _, task := range s.plugin.Tasks {
		taskMeta = append(taskMeta, &api.TaskMetadata{
			Name:        task.Name,
			Verb:        task.Verb,
			Description: task.Description,
			Outputs:     task.Outputs,
		})
	}
	return &api.DescribeResponse{
		Name:       s.plugin.Name,
		Activation: s.plugin.Activation,
		Network:    s.plugin.Network,
		Tasks:      taskMeta,
	}, nil
}

func (s *server) RunTask(stream api.PluginService_RunTaskServer) error {
	firstReq, err := stream.Recv()
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "receiving task start: %v", err)
	}
	start := firstReq.GetStart()
	if start == nil {
		return status.Errorf(codes.InvalidArgument, "first message must be TaskStart")
	}

	task, ok := s.tasks[start.Task]
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
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return status.Errorf(codes.Internal, "receiving file chunk: %v", err)
		}
		chunk := req.GetFile()
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

		info, err := root.Lstat(cleanPath)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return status.Errorf(codes.Internal, "stat file %q: %v", chunk.Path, err)
		}
		if err == nil && !info.Mode().IsRegular() {
			return status.Errorf(codes.InvalidArgument, "file path %q is not a regular file", chunk.Path)
		}

		mode := os.FileMode(chunk.Mode).Perm()
		if mode == 0 {
			mode = 0644
		}

		f, err := root.OpenFile(cleanPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, mode)
		if err != nil {
			return status.Errorf(codes.Internal, "opening file %q: %v", chunk.Path, err)
		}
		if len(chunk.Content) > 0 {
			if _, err := f.Write(chunk.Content); err != nil {
				return status.Errorf(codes.Internal, "writing file %q: %v", chunk.Path, errors.Join(err, f.Close()))
			}
		}
		if err := f.Close(); err != nil {
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
	sendResponse := func(resp *api.RunTaskResponse) error {
		sendMutex.Lock()
		defer sendMutex.Unlock()
		return stream.Send(resp)
	}

	stdoutWriter := &logWriter{streamType: api.LogLine_STDOUT, send: sendResponse}
	stderrWriter := &logWriter{streamType: api.LogLine_STDERR, send: sendResponse}

	taskCtx := TaskContext{
		RootDir:   rootDir,
		Dir:       activatedDir,
		OutputDir: outputDir,
		Stdout:    stdoutWriter,
		Stderr:    stderrWriter,
	}

	taskErr := task.Run(stream.Context(), taskCtx)
	if taskErr != nil {
		exitCode := int32(1)
		var exitErr *exec.ExitError
		if errors.As(taskErr, &exitErr) && exitErr.ExitCode() != 0 {
			exitCode = int32(exitErr.ExitCode())
		}
		return sendResponse(&api.RunTaskResponse{
			Payload: &api.RunTaskResponse_Result{
				Result: &api.TaskResult{
					ExitCode:     exitCode,
					ErrorMessage: taskErr.Error(),
				},
			},
		})
	}

	err = filepath.WalkDir(outputDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		relPath, err := filepath.Rel(outputDir, path)
		if err != nil {
			return err
		}
		slashPath := filepath.ToSlash(relPath)
		return sendOutputFile(path, slashPath, uint32(info.Mode().Perm()), sendResponse)
	})
	if err != nil {
		return status.Errorf(codes.Internal, "streaming output files: %v", err)
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

func (w *logWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	err := w.send(&api.RunTaskResponse{
		Payload: &api.RunTaskResponse_Log{
			Log: &api.LogLine{
				Stream: w.streamType,
				Data:   bytes.Clone(p),
			},
		},
	})
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func sendOutputFile(localPath, relPath string, mode uint32, send func(*api.RunTaskResponse) error) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "failed to close %s: %v\n", localPath, closeErr)
		}
	}()

	buf := make([]byte, maxChunkSize)
	n, err := io.ReadFull(f, buf)
	if errors.Is(err, io.EOF) {
		return send(&api.RunTaskResponse{
			Payload: &api.RunTaskResponse_OutputFile{
				OutputFile: &api.FileChunk{
					Path:        relPath,
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

	currentContent := bytes.Clone(buf[:n])
	nextBuf := make([]byte, maxChunkSize)

	for {
		nextN, nextErr := io.ReadFull(f, nextBuf)
		if errors.Is(nextErr, io.EOF) {
			return send(&api.RunTaskResponse{
				Payload: &api.RunTaskResponse_OutputFile{
					OutputFile: &api.FileChunk{
						Path:        relPath,
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
					Path:        relPath,
					Mode:        mode,
					Content:     currentContent,
					IsLastChunk: false,
				},
			},
		}); err != nil {
			return err
		}

		currentContent = bytes.Clone(nextBuf[:nextN])
	}
}

func isPathEscape(p string) bool {
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") {
		return true
	}
	clean := filepath.Clean(filepath.FromSlash(p))
	return clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || strings.HasPrefix(clean, "../")
}
