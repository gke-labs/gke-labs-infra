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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/sandbox/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/klog/v2"
)

// Run runs the ap command in a sandbox pod.
func Run(ctx context.Context, root string, args []string) error {
	podName := "ap-sandbox"
	image := "golang:1.25-trixie"

	klog.Infof("Ensuring sandbox pod %s is running...", podName)

	// Check if pod exists
	checkCmd := exec.CommandContext(ctx, "kubectl", "get", "pod", podName, "--no-headers")
	if err := checkCmd.Run(); err != nil {
		// Pod doesn't exist, create it
		klog.Infof("Creating pod %s...", podName)
		runCmd := exec.CommandContext(ctx, "kubectl", "run", podName,
			"--image="+image,
			"--restart=Never",
			"--", "sleep", "infinity")
		if err := runCmd.Run(); err != nil {
			return fmt.Errorf("failed to create sandbox pod: %w", err)
		}

		// Wait for pod to be ready
		klog.Infof("Waiting for pod %s to be ready...", podName)
		waitCmd := exec.CommandContext(ctx, "kubectl", "wait", "--for=condition=Ready", "pod/"+podName, "--timeout=60s")
		if err := waitCmd.Run(); err != nil {
			return fmt.Errorf("pod did not become ready: %w", err)
		}
	}

	// Bootstrap: Build and upload 'ap' binary so we can start the gRPC server.
	// We use kubectl exec with stdin to avoid 'kubectl cp'.
	klog.Infof("Bootstrapping ap in pod...")
	apBinary := filepath.Join(os.TempDir(), "ap-sandbox-bin")
	buildCmd := exec.CommandContext(ctx, "go", "build", "-o", apBinary, "./ap")
	buildCmd.Dir = root
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("failed to build ap for bootstrapping: %w", err)
	}
	defer os.Remove(apBinary)

	bootstrapCmd := exec.CommandContext(ctx, "kubectl", "cp", apBinary, podName+":/usr/local/bin/ap")
	if err := bootstrapCmd.Run(); err != nil {
		return fmt.Errorf("failed to upload ap binary to pod: %w", err)
	}

	chmodCmd := exec.CommandContext(ctx, "kubectl", "exec", podName, "--", "chmod", "+x", "/usr/local/bin/ap")
	if err := chmodCmd.Run(); err != nil {
		return fmt.Errorf("failed to chmod ap binary in pod: %w", err)
	}

	// Start the server in the pod
	klog.Infof("Starting ap serve in pod...")
	// Run in background using a shell that disowns the process
	startServerCmd := exec.CommandContext(ctx, "kubectl", "exec", podName, "--", "bash", "-c", "mkdir -p /workspace/src && nohup ap serve --root /workspace/src > /tmp/ap-serve.log 2>&1 &")
	if err := startServerCmd.Run(); err != nil {
		return fmt.Errorf("failed to start ap serve in pod: %w", err)
	}

	// Port forward
	klog.Infof("Setting up port-forward...")
	localPort := 50051
	pfCmd := exec.CommandContext(ctx, "kubectl", "port-forward", "pod/"+podName, fmt.Sprintf("%d:%d", localPort, localPort))
	// Redirect pf output to avoid noise
	pfCmd.Stdout = nil
	pfCmd.Stderr = nil
	if err := pfCmd.Start(); err != nil {
		return fmt.Errorf("failed to start port-forward: %w", err)
	}
	defer func() {
		if pfCmd.Process != nil {
			pfCmd.Process.Kill()
		}
	}()

	// Wait for port-forward to be ready by trying to connect
	var conn *grpc.ClientConn
	var err error
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
	defer conn.Close()
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
			fullPath := filepath.Join(root, file.Path)
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
