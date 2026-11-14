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
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sandbox/api"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tool/kubectl"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/klog/v2"
)

// Run runs the ap command in a sandbox pod.
func Run(ctx context.Context, root string, args []string) (retErr error) {
	podName := "ap-sandbox"
	image := "local/ap-golang:latest"

	kubectlBin, err := kubectl.Spec.EnsureForRepo(ctx, root)
	if err != nil {
		return fmt.Errorf("failed to ensure kubectl: %w", err)
	}

	klog.Infof("Ensuring sandbox pod %s is running...", podName)

	// Check if pod exists
	checkCmd := exec.CommandContext(ctx, kubectlBin, "get", "pod", podName, "--no-headers")
	if err := checkCmd.Run(); err != nil {
		// Pod doesn't exist, create it
		klog.Infof("Creating pod %s...", podName)
		runCmd := exec.CommandContext(ctx, kubectlBin, "run", podName,
			"--image="+image,
			"--restart=Never",
			"--", "serve")
		if err := runCmd.Run(); err != nil {
			return fmt.Errorf("failed to create sandbox pod: %w", err)
		}

		// Wait for pod to be ready
		klog.Infof("Waiting for pod %s to be ready...", podName)
		waitCmd := exec.CommandContext(ctx, kubectlBin, "wait", "--for=condition=Ready", "pod/"+podName, "--timeout=60s")
		if err := waitCmd.Run(); err != nil {
			return fmt.Errorf("pod did not become ready: %w", err)
		}
	}

	// Port forward
	klog.Infof("Setting up port-forward...")
	localPort := 50051
	pfCmd := exec.CommandContext(ctx, kubectlBin, "port-forward", "pod/"+podName, fmt.Sprintf("%d:%d", localPort, localPort))
	// Redirect pf output to avoid noise
	pfCmd.Stdout = nil
	pfCmd.Stderr = nil
	if err := pfCmd.Start(); err != nil {
		return fmt.Errorf("failed to start port-forward: %w", err)
	}
	defer func() {
		if pfCmd.Process != nil {
			if err := pfCmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				retErr = errors.Join(retErr, fmt.Errorf("failed to kill port-forward process: %w", err))
			}
		}
	}()

	// Wait for port-forward to be ready by trying to connect
	var conn *grpc.ClientConn
	for i := 0; i < 10; i++ {
		conn, err = grpc.Dial(fmt.Sprintf("localhost:%d", localPort), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock(), grpc.WithTimeout(1*time.Second))
		if err == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}
	if err != nil {
		return fmt.Errorf("failed to connect to sandbox gRPC after retries: %w", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			klog.Warningf("failed to close grpc connection: %v", err)
		}
	}()
	client := api.NewSandboxServiceClient(conn)

	// Copy code using gRPC
	klog.Infof("Copying code to sandbox using gRPC...")
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == ".build" || info.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}

		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		_, err = client.WriteFile(ctx, &api.WriteFileRequest{
			Path:    relPath,
			Content: content,
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to sync code to sandbox: %w", err)
	}

	// Run the task
	klog.Infof("Executing task: ap %s", strings.Join(args, " "))
	resp, err := client.RunTask(ctx, &api.RunTaskRequest{
		Args: args,
	})
	if err != nil {
		return fmt.Errorf("failed to execute task: %w", err)
	}

	fmt.Print(resp.Stdout)
	fmt.Fprint(os.Stderr, resp.Stderr)

	// Copy back changed files/results
	if len(resp.ChangedFiles) > 0 {
		klog.Infof("Copying back %d changed files...", len(resp.ChangedFiles))
		for _, file := range resp.ChangedFiles {
			fullPath := filepath.Clean(filepath.Join(root, file.Path))
			rel, err := filepath.Rel(root, fullPath)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("sandbox returned path %q escaping root", file.Path)
			}
			if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
				return fmt.Errorf("failed to create local directory for %s: %w", file.Path, err)
			}
			if err := os.WriteFile(fullPath, file.Content, 0644); err != nil {
				return fmt.Errorf("failed to write local file %s: %w", file.Path, err)
			}
		}
	}

	if resp.ExitCode != 0 {
		return fmt.Errorf("sandbox command failed with exit code %d", resp.ExitCode)
	}

	return nil
}
