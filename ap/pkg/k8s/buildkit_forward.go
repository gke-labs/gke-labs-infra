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
	"sync/atomic"

	"github.com/go-logr/logr"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// BuildkitForwarder forwards connections from a private unix domain socket to
// port 2375 of an in-cluster BuildKit pod via the Kubernetes port-forward API.
type BuildkitForwarder struct {
	logger           logr.Logger
	socketDirectory string
	socketPath      string
	remotePort      int
	listener        net.Listener
	streamConnection httpstream.Connection
	stopChannel     chan struct{}
	closeOnce       sync.Once
	requestID       atomic.Uint32
}

// NewBuildkitForwarder resolves a ready pod behind service in namespace on cluster,
// opens an in-process port-forward to port 2375, and listens on a private unix domain socket.
func NewBuildkitForwarder(ctx context.Context, cluster KubeCluster, namespace, service string) (*BuildkitForwarder, error) {
	config, err := cluster.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading client configuration for cluster %s: %w", cluster, err)
	}

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("adding core/v1 to scheme: %w", err)
	}
	codecs := serializer.NewCodecFactory(scheme)

	restConfig := *config
	groupVersion := corev1.SchemeGroupVersion
	restConfig.GroupVersion = &groupVersion
	restConfig.APIPath = "/api"
	restConfig.NegotiatedSerializer = codecs.WithoutConversion()
	if restConfig.UserAgent == "" {
		restConfig.UserAgent = rest.DefaultKubernetesUserAgent()
	}

	restClient, err := rest.RESTClientFor(&restConfig)
	if err != nil {
		return nil, fmt.Errorf("creating rest client: %w", err)
	}

	pod, err := findReadyPod(ctx, restClient, namespace, service)
	if err != nil {
		return nil, err
	}

	request := restClient.Post().
		Resource("pods").
		Namespace(namespace).
		Name(pod.Name).
		SubResource("portforward")

	transport, upgrader, err := spdy.RoundTripperFor(config)
	if err != nil {
		return nil, fmt.Errorf("creating spdy roundtripper: %w", err)
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, request.URL())
	return newBuildkitForwarderWithDialer(scoped.Log(ctx), dialer, 2375)
}

func findReadyPod(ctx context.Context, client rest.Interface, namespace, service string) (*corev1.Pod, error) {
	serviceObject := &corev1.Service{}
	err := client.Get().
		Resource("services").
		Namespace(namespace).
		Name(service).
		Do(ctx).
		Into(serviceObject)
	if err != nil {
		return nil, fmt.Errorf("getting service %s/%s: %w", namespace, service, err)
	}
	if len(serviceObject.Spec.Selector) == 0 {
		return nil, fmt.Errorf("no ready pod found for service %s/%s", namespace, service)
	}

	selector := labels.SelectorFromSet(serviceObject.Spec.Selector).String()
	podList := &corev1.PodList{}
	err = client.Get().
		Resource("pods").
		Namespace(namespace).
		VersionedParams(&metav1.ListOptions{LabelSelector: selector}, metav1.ParameterCodec).
		Do(ctx).
		Into(podList)
	if err != nil {
		return nil, fmt.Errorf("listing pods for service %s/%s: %w", namespace, service, err)
	}

	for i := range podList.Items {
		pod := &podList.Items[i]
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

func newBuildkitForwarderWithDialer(logger logr.Logger, dialer httpstream.Dialer, remotePort int) (*BuildkitForwarder, error) {
	streamConnection, protocol, err := dialer.Dial(portforward.PortForwardProtocolV1Name)
	if err != nil {
		return nil, fmt.Errorf("dialing port-forward stream: %w", err)
	}
	if protocol != portforward.PortForwardProtocolV1Name {
		if closeErr := streamConnection.Close(); closeErr != nil {
			return nil, fmt.Errorf("unexpected negotiated protocol %q: %w", protocol, closeErr)
		}
		return nil, fmt.Errorf("unexpected negotiated protocol: %q", protocol)
	}

	socketDirectory, err := os.MkdirTemp("/tmp", "ap-bk-*")
	if err != nil {
		if closeErr := streamConnection.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return nil, fmt.Errorf("creating temporary directory for unix socket: %w", err)
	}

	socketPath := filepath.Join(socketDirectory, "buildkit.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		if closeErr := streamConnection.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		if removeErr := os.RemoveAll(socketDirectory); removeErr != nil {
			err = errors.Join(err, removeErr)
		}
		return nil, fmt.Errorf("listening on unix socket %s: %w", socketPath, err)
	}

	forwarder := &BuildkitForwarder{
		logger:           logger,
		socketDirectory: socketDirectory,
		socketPath:      socketPath,
		remotePort:      remotePort,
		listener:        listener,
		streamConnection: streamConnection,
		stopChannel:     make(chan struct{}),
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
		close(f.stopChannel)
		if err := f.listener.Close(); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
		if err := f.streamConnection.Close(); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
		if err := os.RemoveAll(f.socketDirectory); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
	})
	return returnErr
}

func (f *BuildkitForwarder) acceptLoop() {
	for {
		connection, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.handleConnection(connection)
	}
}

func (f *BuildkitForwarder) handleConnection(connection net.Conn) {
	defer func() {
		if closeErr := connection.Close(); closeErr != nil {
			f.logger.V(4).Info("closing port-forward connection", "error", closeErr)
		}
	}()

	requestID := f.requestID.Add(1)

	headers := http.Header{}
	headers.Set(corev1.StreamType, corev1.StreamTypeError)
	headers.Set(corev1.PortHeader, strconv.Itoa(f.remotePort))
	headers.Set(corev1.PortForwardRequestIDHeader, strconv.FormatUint(uint64(requestID), 10))

	errorStream, err := f.streamConnection.CreateStream(headers)
	if err != nil {
		f.logger.Info("failed creating error stream for port-forward", "error", err)
		return
	}
	if closeErr := errorStream.Close(); closeErr != nil {
		f.logger.V(4).Info("closing error stream for port-forward", "error", closeErr)
		return
	}
	defer f.streamConnection.RemoveStreams(errorStream)

	errorChannel := make(chan struct{})
	go func() {
		defer close(errorChannel)
		message, readErr := io.ReadAll(errorStream)
		if readErr != nil {
			f.logger.Info("failed reading port-forward error stream", "error", readErr)
			return
		}
		if len(message) > 0 {
			f.logger.Info("port-forward error from pod", "message", string(message))
		}
	}()

	headers.Set(corev1.StreamType, corev1.StreamTypeData)
	dataStream, err := f.streamConnection.CreateStream(headers)
	if err != nil {
		f.logger.Info("failed creating data stream for port-forward", "error", err)
		return
	}
	defer f.streamConnection.RemoveStreams(dataStream)

	localError := make(chan struct{})
	remoteDone := make(chan struct{})

	go func() {
		defer close(remoteDone)
		if _, copyErr := io.Copy(connection, dataStream); copyErr != nil {
			f.logger.V(4).Info("copying from remote data stream to unix connection", "error", copyErr)
		}
	}()

	go func() {
		defer func() {
			if closeErr := dataStream.Close(); closeErr != nil {
				f.logger.V(4).Info("closing data stream", "error", closeErr)
			}
		}()
		if _, copyErr := io.Copy(dataStream, connection); copyErr != nil {
			close(localError)
		}
	}()

	select {
	case <-remoteDone:
	case <-localError:
	case <-f.stopChannel:
	}

	if resetErr := dataStream.Reset(); resetErr != nil {
		f.logger.V(4).Info("resetting data stream", "error", resetErr)
	}
	<-errorChannel
}
