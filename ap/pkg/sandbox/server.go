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

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sandbox/api"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"google.golang.org/grpc"
)

type server struct {
	api.UnimplementedSandboxServiceServer
	root string
}

func (s *server) WriteFile(_ context.Context, req *api.WriteFileRequest) (*api.WriteFileResponse, error) {
	fullPath := filepath.Join(s.root, req.Path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}
	if err := os.WriteFile(fullPath, req.Content, 0644); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}
	return &api.WriteFileResponse{}, nil
}

func (s *server) ReadFile(_ context.Context, req *api.ReadFileRequest) (*api.ReadFileResponse, error) {
	fullPath := filepath.Join(s.root, req.Path)
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	return &api.ReadFileResponse{Content: content}, nil
}

func (s *server) RunTask(ctx context.Context, req *api.RunTaskRequest) (*api.RunTaskResponse, error) {
	scoped.Log(ctx).Info("running task in sandbox", "command", "ap "+strings.Join(req.Args, " "))

	startTime := time.Now()

	// We assume 'ap' is in the PATH in the sandbox pod.
	cmd := exec.CommandContext(ctx, "ap", req.Args...)
	cmd.Dir = s.root
	cmd.Env = append(os.Environ(), "AP_ROOT="+s.root)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		} else {
			return nil, fmt.Errorf("failed to run ap: %w", err)
		}
	}

	resp := &api.RunTaskResponse{
		ExitCode: int32(exitCode),
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}

	// Hard-coded logic to return changed files or results
	if len(req.Args) > 0 {
		switch req.Args[0] {
		case "test":
			// Copy back .build/test-results
			resultsDir := filepath.Join(s.root, ".build", "test-results")
			err := filepath.Walk(resultsDir, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() {
					return nil
				}
				relPath, err := filepath.Rel(s.root, path)
				if err != nil {
					return err
				}
				content, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				resp.ChangedFiles = append(resp.ChangedFiles, &api.ChangedFile{
					Path:    relPath,
					Content: content,
				})
				return nil
			})
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("failed to walk test results: %w", err)
			}
		case "format", "fmt":
			// Return all files modified after startTime
			err := filepath.Walk(s.root, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() {
					return nil
				}
				if info.ModTime().After(startTime) {
					relPath, err := filepath.Rel(s.root, path)
					if err != nil {
						return err
					}
					content, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					resp.ChangedFiles = append(resp.ChangedFiles, &api.ChangedFile{
						Path:    relPath,
						Content: content,
					})
				}
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("failed to walk sandbox root: %w", err)
			}
		}
	}

	return resp, nil
}

// Serve starts the gRPC server.
func Serve(ctx context.Context, root string, port int) error {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	s := grpc.NewServer()
	api.RegisterSandboxServiceServer(s, &server{root: root})

	scoped.Log(ctx).Info("sandbox server listening", "address", lis.Addr().String())

	go func() {
		<-ctx.Done()
		s.GracefulStop()
	}()

	return s.Serve(lis)
}
