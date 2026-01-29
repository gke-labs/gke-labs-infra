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
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/generate"
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
		runCmd.Stdout = os.Stdout
		runCmd.Stderr = os.Stderr
		if err := runCmd.Run(); err != nil {
			return fmt.Errorf("failed to create sandbox pod: %w", err)
		}

		// Wait for pod to be ready (only if we just created it)
		klog.Infof("Waiting for pod %s to be ready...", podName)
		waitCmd := exec.CommandContext(ctx, "kubectl", "wait", "--for=condition=Ready", "pod/"+podName, "--timeout=60s")
		waitCmd.Stdout = os.Stdout
		waitCmd.Stderr = os.Stderr
		if err := waitCmd.Run(); err != nil {
			return fmt.Errorf("pod did not become ready: %w", err)
		}
	}

	// Ensure parent directory exists in the pod, but NOT the src directory itself
	// so that kubectl cp . pod:/workspace/src creates src from .
	klog.Infof("Preparing directory in pod...")
	mkdirCmd := exec.CommandContext(ctx, "kubectl", "exec", podName, "--", "mkdir", "-p", "/workspace")
	if err := mkdirCmd.Run(); err != nil {
		return fmt.Errorf("failed to create directory in pod: %w", err)
	}

	// Copy code to pod
	klog.Infof("Copying code from %s to pod...", root)
	// Note: we use "." to refer to the current directory which should be the root passed in.
	// But it's safer to use the 'root' variable.
	cpCmd := exec.CommandContext(ctx, "kubectl", "cp", root, podName+":/workspace/src")
	cpCmd.Stdout = os.Stdout
	cpCmd.Stderr = os.Stderr
	if err := cpCmd.Run(); err != nil {
		// kubectl cp often returns non-zero even if it mostly worked (e.g. due to special files)
		klog.Warningf("kubectl cp had some issues, continuing anyway: %v", err)
	}

	// Run the command in the pod
	klog.Infof("Running command in sandbox: ap %s", strings.Join(args, " "))

	apCmd, err := generate.GetApCommand(root)
	if err != nil {
		return fmt.Errorf("failed to get ap command: %w", err)
	}

	// Prepare the command string for bash -c
	goCmd := apCmd
	if len(args) > 0 {
		goCmd += " " + strings.Join(args, " ")
	}

	execCmd := exec.CommandContext(ctx, "kubectl", "exec", podName, "--", "bash", "-c",
		fmt.Sprintf("cd /workspace/src && %s", goCmd))

	execCmd.Stdout = os.Stdout
	execCmd.Stderr = os.Stderr

	if err := execCmd.Run(); err != nil {
		return fmt.Errorf("failed to execute command in sandbox: %w", err)
	}

	return nil
}
