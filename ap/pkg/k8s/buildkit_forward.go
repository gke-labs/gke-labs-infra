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
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
)

// BuildkitForwarder forwards connections from a private unix domain socket to
// port 2375 of an in-cluster BuildKit pod via the Kubernetes port-forward API.
type BuildkitForwarder struct {
	podForwarder    *PodPortForwarder
	logger          *slog.Logger
	socketDirectory string
	socketPath      string
	listener        net.Listener
	stopChannel     chan struct{}
	closeOnce       sync.Once
}

// NewBuildkitForwarder resolves a ready pod behind service in namespace on cluster,
// opens an in-process port-forward to port 2375, and listens on a private unix domain socket.
func NewBuildkitForwarder(ctx context.Context, cluster KubeCluster, namespace, service string) (*BuildkitForwarder, error) {
	forwarder, err := NewPodPortForwarder(ctx, cluster, namespace, service, 2375)
	if err != nil {
		return nil, err
	}
	return newBuildkitForwarder(scoped.Log(ctx), forwarder)
}

func newBuildkitForwarder(logger *slog.Logger, podForwarder *PodPortForwarder) (*BuildkitForwarder, error) {
	socketDirectory, err := os.MkdirTemp("/tmp", "ap-bk-*")
	if err != nil {
		if closeErr := podForwarder.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return nil, fmt.Errorf("creating temporary directory for unix socket: %w", err)
	}

	socketPath := filepath.Join(socketDirectory, "buildkit.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		if closeErr := podForwarder.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		if removeErr := os.RemoveAll(socketDirectory); removeErr != nil {
			err = errors.Join(err, removeErr)
		}
		return nil, fmt.Errorf("listening on unix socket %s: %w", socketPath, err)
	}

	forwarder := &BuildkitForwarder{
		podForwarder:    podForwarder,
		logger:          logger,
		socketDirectory: socketDirectory,
		socketPath:      socketPath,
		listener:        listener,
		stopChannel:     make(chan struct{}),
	}
	go forwarder.acceptLoop()
	return forwarder, nil
}

// SocketPath returns the path to the unix domain socket.
func (forwarder *BuildkitForwarder) SocketPath() string {
	return forwarder.socketPath
}

// Close stops the listener, closes the port-forward connection, and deletes the socket directory.
func (forwarder *BuildkitForwarder) Close() error {
	var returnErr error
	forwarder.closeOnce.Do(func() {
		close(forwarder.stopChannel)
		if err := forwarder.listener.Close(); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
		if err := forwarder.podForwarder.Close(); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
		if err := os.RemoveAll(forwarder.socketDirectory); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
	})
	return returnErr
}

func (forwarder *BuildkitForwarder) acceptLoop() {
	for {
		connection, err := forwarder.listener.Accept()
		if err != nil {
			return
		}
		go forwarder.handleConnection(connection)
	}
}

func (forwarder *BuildkitForwarder) handleConnection(connection net.Conn) {
	defer func() {
		if closeErr := connection.Close(); closeErr != nil {
			forwarder.logger.Debug("closing port-forward connection", "error", closeErr)
		}
	}()

	remoteConn, err := forwarder.podForwarder.DialStream()
	if err != nil {
		forwarder.logger.Info("failed opening stream to pod", "error", err)
		return
	}
	defer func() {
		if closeErr := remoteConn.Close(); closeErr != nil {
			forwarder.logger.Debug("closing remote stream connection", "error", closeErr)
		}
	}()

	localError := make(chan struct{})
	remoteDone := make(chan struct{})

	go func() {
		defer close(remoteDone)
		if _, copyErr := io.Copy(connection, remoteConn); copyErr != nil {
			forwarder.logger.Debug("copying from remote data stream to unix connection", "error", copyErr)
		}
	}()

	go func() {
		defer func() {
			if closeErr := remoteConn.Close(); closeErr != nil {
				forwarder.logger.Debug("closing remote connection", "error", closeErr)
			}
		}()
		if _, copyErr := io.Copy(remoteConn, connection); copyErr != nil {
			close(localError)
		}
	}()

	select {
	case <-remoteDone:
	case <-localError:
	case <-forwarder.stopChannel:
	}
}
