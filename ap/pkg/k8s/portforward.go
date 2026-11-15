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

package k8s

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

// PortForwardTask wraps another task and ensures a port-forward is running while it executes.
type PortForwardTask struct {
	Child      tasks.Task
	Service    string
	Namespace  string
	LocalPort  int
	RemotePort int
	Cluster    KubeCluster
}

func (t *PortForwardTask) buildArgs(localPort int) []string {
	var args []string
	args = append(args, t.Cluster.KubectlArgs()...)
	args = append(args,
		"port-forward",
		"-n", t.Namespace,
		"svc/"+t.Service,
		fmt.Sprintf("%d:%d", localPort, t.RemotePort),
	)
	return args
}

func (t *PortForwardTask) Run(ctx context.Context, scope *tasks.TaskContext) (retErr error) {
	if IsInCluster() {
		scope.Log().Info("running in-cluster, skipping port-forward", "namespace", t.Namespace, "service", t.Service)
		return t.Child.Run(ctx, scope)
	}

	// On macOS the Docker daemon runs in a Linux VM and cannot reach a
	// port-forward bound to localhost on the host, so pushes to
	// localhost:5000 time out. We bridge with a socat container that
	// publishes port 5000 inside the VM and forwards to the host via
	// host.docker.internal. The published port also claims 127.0.0.1:5000 on
	// the host, so kubectl must forward on a different local port or the two
	// would fight over the same bind (and socat would loop back into its own
	// published port).
	useProxy := runtime.GOOS == "darwin" && t.LocalPort == 5000
	if useProxy {
		if _, err := exec.LookPath("docker"); err != nil {
			useProxy = false
		}
	}

	localPort := t.LocalPort
	if useProxy {
		// A stale proxy container holds 127.0.0.1:5000 on the host; remove it
		// before anything tries to bind.
		if err := exec.CommandContext(ctx, "docker", "rm", "-f", "ap-registry-proxy").Run(); err != nil {
			return fmt.Errorf("failed to remove stale docker proxy container: %w", err)
		}

		freePort, err := PickFreePort()
		if err != nil {
			return fmt.Errorf("finding free local port for port-forward: %w", err)
		}
		localPort = freePort
	}

	kubectlBin, err := scope.Tool(ctx, "kubectl")
	if err != nil {
		return fmt.Errorf("failed to ensure kubectl: %w", err)
	}
	if kubectlBin == "" {
		return fmt.Errorf("kubectl binary is required")
	}

	scope.Log().Info("starting port-forward", "namespace", t.Namespace, "service", t.Service, "localPort", localPort, "remotePort", t.RemotePort)

	pfCmd := exec.CommandContext(ctx, kubectlBin, t.buildArgs(localPort)...)

	var stdout, stderr syncBuffer
	pfCmd.Stdout = &stdout
	pfCmd.Stderr = &stderr

	if err := pfCmd.Start(); err != nil {
		return fmt.Errorf("failed to start port-forward: %w", err)
	}

	var hasProxy bool
	defer func() {
		if pfCmd.Process != nil {
			if err := pfCmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				retErr = errors.Join(retErr, fmt.Errorf("failed to kill port-forward process: %w", err))
			}
		}
		if hasProxy {
			scope.Log().Info("stopping docker registry proxy container")
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := exec.CommandContext(cleanupCtx, "docker", "rm", "-f", "ap-registry-proxy").Run(); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("failed to stop docker registry proxy container: %w", err))
			}
		}
	}()

	pfExited := make(chan error, 1)
	go func() { pfExited <- pfCmd.Wait() }()

	// Wait for kubectl to report the forward is listening. We check its
	// output rather than dialing the port: on macOS other listeners (e.g.
	// AirPlay on *:5000) accept connections and mask a failed bind.
	ready := false
	for i := 0; i < 80; i++ {
		if strings.Contains(stdout.String(), "Forwarding from") {
			ready = true
			break
		}
		select {
		case err := <-pfExited:
			logPortForwardOutput(ctx, &stdout, &stderr)
			return fmt.Errorf("port-forward to %s/%s exited before becoming ready: %v", t.Namespace, t.Service, err)
		case <-time.After(250 * time.Millisecond):
		}
	}

	if !ready {
		scope.Log().Error(nil, "port-forward did not become ready", "namespace", t.Namespace, "service", t.Service)
		logPortForwardOutput(ctx, &stdout, &stderr)
		return fmt.Errorf("port-forward did not become ready")
	}

	if useProxy {
		scope.Log().Info("starting docker registry proxy container", "vmPort", t.LocalPort, "hostPort", localPort)
		proxyCmd := exec.CommandContext(ctx, "docker", "run", "-d",
			"--name", "ap-registry-proxy",
			"-p", fmt.Sprintf("127.0.0.1:%d:%d", t.LocalPort, t.LocalPort),
			"alpine/socat",
			fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr", t.LocalPort),
			fmt.Sprintf("TCP:host.docker.internal:%d", localPort))
		if out, err := proxyCmd.CombinedOutput(); err != nil {
			// The port-forward is on localPort, so without the proxy nothing
			// serves localhost:5000 and the push cannot succeed.
			return fmt.Errorf("failed to start docker registry proxy: %w (output: %s)", err, out)
		}
		hasProxy = true
		scope.Log().Info("docker registry proxy container started successfully")
	}

	scope.Log().Info("port-forward ready, running child task", "child", t.Child.GetName())
	return t.Child.Run(ctx, scope)
}

// PickFreePort returns a TCP port that is currently free on 127.0.0.1.
func PickFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() {
		if err := l.Close(); err != nil {
			scoped.Log(context.Background()).Info("failed to close listener", "level", "warning", "error", err)
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func logPortForwardOutput(ctx context.Context, stdout, stderr *syncBuffer) {
	if s := stdout.String(); s != "" {
		scoped.Log(ctx).Error(nil, "kubectl port-forward stdout", "output", s)
	}
	if s := stderr.String(); s != "" {
		scoped.Log(ctx).Error(nil, "kubectl port-forward stderr", "output", s)
	}
}

// syncBuffer is a bytes.Buffer safe for concurrent use; exec.Cmd writes to it
// from a copier goroutine while we poll it for readiness.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (t *PortForwardTask) GetName() string {
	return fmt.Sprintf("port-forward-%s", t.Child.GetName())
}

func (t *PortForwardTask) GetChildren() []tasks.Task {
	return []tasks.Task{t.Child}
}

func (t *PortForwardTask) WithChildren(children []tasks.Task) tasks.Task {
	if len(children) == 0 {
		return nil
	}
	cp := *t
	cp.Child = children[0]
	return &cp
}

var _ tasks.Parent = (*PortForwardTask)(nil)
