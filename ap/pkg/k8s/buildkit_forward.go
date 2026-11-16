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
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// BuildkitForwarder forwards connections from a private unix domain socket to
// port 2375 of an in-cluster BuildKit pod via the Kubernetes port-forward API.
type BuildkitForwarder struct {
	socketDirectory string
	socketPath      string
	remotePort      int
	listener        net.Listener
	streamConn      httpstream.Connection
	stopChan        chan struct{}
	closeOnce       sync.Once
	requestID       int
	mu              sync.Mutex
}

// NewBuildkitForwarder resolves a ready pod behind service in namespace on cluster,
// opens an in-process port-forward to port 2375, and listens on a private unix domain socket.
func NewBuildkitForwarder(ctx context.Context, cluster KubeCluster, namespace, service string) (*BuildkitForwarder, error) {
	config, err := cluster.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading client configuration for cluster %s: %w", cluster, err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}

	pod, err := findReadyPod(ctx, clientset, namespace, service)
	if err != nil {
		return nil, err
	}

	request := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(pod.Name).
		SubResource("portforward")

	transport, upgrader, err := spdy.RoundTripperFor(config)
	if err != nil {
		return nil, fmt.Errorf("creating spdy roundtripper: %w", err)
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, request.URL())
	return NewBuildkitForwarderWithDialer(dialer, 2375)
}

func findReadyPod(ctx context.Context, clientset kubernetes.Interface, namespace, service string) (*corev1.Pod, error) {
	serviceObj, err := clientset.CoreV1().Services(namespace).Get(ctx, service, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting service %s/%s: %w", namespace, service, err)
	}
	if len(serviceObj.Spec.Selector) == 0 {
		return nil, fmt.Errorf("no ready pod found for service %s/%s", namespace, service)
	}

	selector := labels.SelectorFromSet(serviceObj.Spec.Selector).String()
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("listing pods for service %s/%s: %w", namespace, service, err)
	}

	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				return pod, nil
			}
		}
	}
	return nil, fmt.Errorf("no ready pod found for service %s/%s", namespace, service)
}

// NewBuildkitForwarderWithDialer initializes a BuildkitForwarder with a given dialer and remote port.
func NewBuildkitForwarderWithDialer(dialer httpstream.Dialer, remotePort int) (*BuildkitForwarder, error) {
	socketDirectory, err := os.MkdirTemp("/tmp", "ap-bk-*")
	if err != nil {
		socketDirectory, err = os.MkdirTemp("", "ap-bk-*")
		if err != nil {
			return nil, fmt.Errorf("creating temporary directory for unix socket: %w", err)
		}
	}

	socketPath := filepath.Join(socketDirectory, "buildkit.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		if removeErr := os.RemoveAll(socketDirectory); removeErr != nil {
			err = errors.Join(err, removeErr)
		}
		return nil, fmt.Errorf("listening on unix socket %s: %w", socketPath, err)
	}

	forwarder := &BuildkitForwarder{
		socketDirectory: socketDirectory,
		socketPath:      socketPath,
		remotePort:      remotePort,
		listener:        listener,
		stopChan:        make(chan struct{}),
	}

	streamConn, protocol, err := dialer.Dial(portforward.PortForwardProtocolV1Name)
	if err != nil {
		if closeErr := forwarder.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return nil, fmt.Errorf("dialing port-forward stream: %w", err)
	}
	forwarder.streamConn = streamConn

	if protocol != portforward.PortForwardProtocolV1Name {
		protocolErr := fmt.Errorf("unexpected negotiated protocol: %q", protocol)
		if closeErr := forwarder.Close(); closeErr != nil {
			protocolErr = errors.Join(protocolErr, closeErr)
		}
		return nil, protocolErr
	}

	go forwarder.acceptLoop()
	return forwarder, nil
}

// SocketPath returns the path to the unix domain socket.
func (f *BuildkitForwarder) SocketPath() string {
	return f.socketPath
}

// Close stops the listener, closes the port-forward connection, and deletes the socket directory.
func (f *BuildkitForwarder) Close() error {
	var returnErr error
	f.closeOnce.Do(func() {
		close(f.stopChan)
		if f.listener != nil {
			if err := f.listener.Close(); err != nil {
				returnErr = errors.Join(returnErr, err)
			}
		}
		if f.streamConn != nil {
			if err := f.streamConn.Close(); err != nil {
				returnErr = errors.Join(returnErr, err)
			}
		}
		if f.socketDirectory != "" {
			if err := os.RemoveAll(f.socketDirectory); err != nil {
				returnErr = errors.Join(returnErr, err)
			}
		}
	})
	return returnErr
}

func (f *BuildkitForwarder) acceptLoop() {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.handleConn(conn)
	}
}

func (f *BuildkitForwarder) handleConn(conn net.Conn) {
	defer func() {
		if err := conn.Close(); err != nil {
			// connection closed
		}
	}()

	f.mu.Lock()
	requestID := f.requestID
	f.requestID++
	f.mu.Unlock()

	headers := http.Header{}
	headers.Set(corev1.StreamType, corev1.StreamTypeError)
	headers.Set(corev1.PortHeader, strconv.Itoa(f.remotePort))
	headers.Set(corev1.PortForwardRequestIDHeader, strconv.Itoa(requestID))

	errorStream, err := f.streamConn.CreateStream(headers)
	if err != nil {
		return
	}
	if err := errorStream.Close(); err != nil {
		return
	}
	defer f.streamConn.RemoveStreams(errorStream)

	errorChan := make(chan struct{})
	go func() {
		if _, err := io.ReadAll(errorStream); err != nil {
			// error stream read completed
		}
		close(errorChan)
	}()

	headers.Set(corev1.StreamType, corev1.StreamTypeData)
	dataStream, err := f.streamConn.CreateStream(headers)
	if err != nil {
		return
	}
	defer f.streamConn.RemoveStreams(dataStream)

	localError := make(chan struct{})
	remoteDone := make(chan struct{})

	go func() {
		if _, err := io.Copy(conn, dataStream); err != nil {
			// remote to local copy finished
		}
		close(remoteDone)
	}()

	go func() {
		defer func() {
			if err := dataStream.Close(); err != nil {
				// data stream closed
			}
		}()
		if _, err := io.Copy(dataStream, conn); err != nil {
			close(localError)
		}
	}()

	select {
	case <-remoteDone:
	case <-localError:
	case <-f.stopChan:
	}

	if err := dataStream.Reset(); err != nil {
		// data stream reset
	}
	<-errorChan
}
