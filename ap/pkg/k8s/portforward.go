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
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"k8s.io/klog/v2"
)

// PortForwardTask wraps another task and ensures a port-forward is running while it executes.
type PortForwardTask struct {
	Child      tasks.Task
	Service    string
	Namespace  string
	LocalPort  int
	RemotePort int
}

func (t *PortForwardTask) Run(ctx context.Context, scope *tasks.APScope) error {
	if IsInCluster() {
		klog.Infof("Running in-cluster, skipping port-forward to %s/%s", t.Namespace, t.Service)
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
		exec.CommandContext(ctx, "docker", "rm", "-f", "ap-registry-proxy").Run()

		freePort, err := pickFreePort()
		if err != nil {
			return fmt.Errorf("finding free local port for port-forward: %w", err)
		}
		localPort = freePort
	}

	klog.Infof("Starting port-forward to %s/%s (%d:%d)...", t.Namespace, t.Service, localPort, t.RemotePort)

	pfCmd := exec.CommandContext(ctx, "kubectl", "port-forward",
		"-n", t.Namespace,
		"svc/"+t.Service,
		fmt.Sprintf("%d:%d", localPort, t.RemotePort))

	var stdout, stderr syncBuffer
	pfCmd.Stdout = &stdout
	pfCmd.Stderr = &stderr

	if err := pfCmd.Start(); err != nil {
		return fmt.Errorf("failed to start port-forward: %w", err)
	}

	var hasProxy bool
	defer func() {
		if pfCmd.Process != nil {
			pfCmd.Process.Kill()
		}
		if hasProxy {
			klog.Infof("Stopping docker registry proxy container...")
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			exec.CommandContext(cleanupCtx, "docker", "rm", "-f", "ap-registry-proxy").Run()
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
			logPortForwardOutput(&stdout, &stderr)
			return fmt.Errorf("port-forward to %s/%s exited before becoming ready: %v", t.Namespace, t.Service, err)
		case <-time.After(250 * time.Millisecond):
		}
	}

	if !ready {
		klog.Errorf("port-forward to %s/%s did not become ready", t.Namespace, t.Service)
		logPortForwardOutput(&stdout, &stderr)
		return fmt.Errorf("port-forward did not become ready")
	}

	if useProxy {
		klog.Infof("Starting docker registry proxy container (VM localhost:%d -> host localhost:%d)...", t.LocalPort, localPort)
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
		klog.Infof("Docker registry proxy container started successfully.")
	}

	klog.Infof("Port-forward ready, running child task %s", t.Child.GetName())
	return t.Child.Run(ctx, scope)
}

// pickFreePort returns a TCP port that is currently free on 127.0.0.1.
func pickFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func logPortForwardOutput(stdout, stderr *syncBuffer) {
	if s := stdout.String(); s != "" {
		klog.Errorf("kubectl port-forward stdout: %s", s)
	}
	if s := stderr.String(); s != "" {
		klog.Errorf("kubectl port-forward stderr: %s", s)
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
