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
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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

// PodPortForwarder manages an in-process port-forward connection to a pod and
// creates bidirectional net.Conn streams to a target remote port.
type PodPortForwarder struct {
	logger           *slog.Logger
	podName          string
	remotePort       int
	streamConnection httpstream.Connection
	requestID        atomic.Uint32
	closeOnce        sync.Once
}

// NewPodPortForwarder resolves a ready pod behind service in namespace on cluster,
// opens an in-process port-forward connection to remotePort, and returns a PodPortForwarder.
func NewPodPortForwarder(ctx context.Context, cluster KubeCluster, namespace, service string, remotePort int) (*PodPortForwarder, error) {
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

	pod, err := FindReadyPod(ctx, restClient, namespace, service)
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
	return NewPodPortForwarderWithDialer(scoped.Log(ctx), dialer, pod.Name, remotePort)
}

// NewPodPortForwarderWithDialer connects to remotePort on podName using the provided dialer.
func NewPodPortForwarderWithDialer(logger *slog.Logger, dialer httpstream.Dialer, podName string, remotePort int) (*PodPortForwarder, error) {
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

	return &PodPortForwarder{
		logger:           logger,
		podName:          podName,
		remotePort:       remotePort,
		streamConnection: streamConnection,
	}, nil
}

// FindReadyPod finds a ready pod belonging to service in namespace using client.
func FindReadyPod(ctx context.Context, client rest.Interface, namespace, service string) (*corev1.Pod, error) {
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

	var foundNames []string
	for index := range podList.Items {
		pod := &podList.Items[index]
		if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
			foundNames = append(foundNames, pod.Name)
			continue
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				return pod, nil
			}
		}
		foundNames = append(foundNames, pod.Name)
	}
	if len(foundNames) > 0 {
		return nil, fmt.Errorf("no ready pod found for service %s/%s (found %s)", namespace, service, strings.Join(foundNames, ", "))
	}
	return nil, fmt.Errorf("no ready pod found for service %s/%s", namespace, service)
}

// PodName returns the name of the remote pod.
func (forwarder *PodPortForwarder) PodName() string {
	return forwarder.podName
}

// RemotePort returns the forwarded remote port.
func (forwarder *PodPortForwarder) RemotePort() int {
	return forwarder.remotePort
}

// Transport returns an http.RoundTripper that dials streams through the port-forward connection.
func (forwarder *PodPortForwarder) Transport() http.RoundTripper {
	return &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return forwarder.DialStream()
		},
	}
}

// DialStream creates a new bidirectional net.Conn stream to the pod's remote port.
func (forwarder *PodPortForwarder) DialStream() (net.Conn, error) {
	requestID := forwarder.requestID.Add(1)

	headers := http.Header{}
	headers.Set(corev1.StreamType, corev1.StreamTypeError)
	headers.Set(corev1.PortHeader, strconv.Itoa(forwarder.remotePort))
	headers.Set(corev1.PortForwardRequestIDHeader, strconv.FormatUint(uint64(requestID), 10))

	errorStream, err := forwarder.streamConnection.CreateStream(headers)
	if err != nil {
		return nil, fmt.Errorf("stream refused: %w", err)
	}
	if closeErr := errorStream.Close(); closeErr != nil {
		forwarder.logger.Debug("closing error stream for port-forward", "error", closeErr)
	}

	headers.Set(corev1.StreamType, corev1.StreamTypeData)
	dataStream, err := forwarder.streamConnection.CreateStream(headers)
	if err != nil {
		forwarder.streamConnection.RemoveStreams(errorStream)
		return nil, fmt.Errorf("stream refused: %w", err)
	}

	return newPortForwardConn(forwarder.logger, forwarder.streamConnection, dataStream, errorStream, forwarder.podName, forwarder.remotePort), nil
}

// Close closes the underlying port-forward stream connection.
func (forwarder *PodPortForwarder) Close() error {
	var returnErr error
	forwarder.closeOnce.Do(func() {
		returnErr = forwarder.streamConnection.Close()
	})
	return returnErr
}

type portForwardConn struct {
	logger           *slog.Logger
	streamConnection httpstream.Connection
	dataStream       httpstream.Stream
	errorStream      httpstream.Stream
	errorDone        chan struct{}
	podName          string
	remotePort       int
	closeOnce        sync.Once
}

func newPortForwardConn(logger *slog.Logger, streamConnection httpstream.Connection, dataStream, errorStream httpstream.Stream, podName string, remotePort int) *portForwardConn {
	conn := &portForwardConn{
		logger:           logger,
		streamConnection: streamConnection,
		dataStream:       dataStream,
		errorStream:      errorStream,
		errorDone:        make(chan struct{}),
		podName:          podName,
		remotePort:       remotePort,
	}
	go func() {
		defer close(conn.errorDone)
		message, readErr := io.ReadAll(errorStream)
		if readErr != nil {
			conn.logger.Debug("reading port-forward error stream", "error", readErr)
		}
		if len(message) > 0 {
			conn.logger.Warn("port-forward error from pod", "message", strings.TrimSpace(string(message)))
		}
	}()
	return conn
}

func (conn *portForwardConn) Read(buffer []byte) (int, error) {
	return conn.dataStream.Read(buffer)
}

func (conn *portForwardConn) Write(buffer []byte) (int, error) {
	return conn.dataStream.Write(buffer)
}

// Close closes the data stream, waits for the error stream reader to finish,
// and removes streams from the connection.
func (conn *portForwardConn) Close() error {
	var returnErr error
	conn.closeOnce.Do(func() {
		if closeErr := conn.dataStream.Close(); closeErr != nil {
			returnErr = closeErr
		}
		<-conn.errorDone
		conn.streamConnection.RemoveStreams(conn.dataStream, conn.errorStream)
	})
	return returnErr
}

func (conn *portForwardConn) LocalAddr() net.Addr {
	return portForwardAddr{podName: "local", port: 0}
}

func (conn *portForwardConn) RemoteAddr() net.Addr {
	return portForwardAddr{podName: conn.podName, port: conn.remotePort}
}

// SetDeadline, SetReadDeadline, and SetWriteDeadline are no-ops returning nil.
// Deadlines are not supported on SPDY multiplexed streams; cancellation closes
// the underlying port-forward connection, which immediately terminates and fails
// any pending reads and writes.
func (conn *portForwardConn) SetDeadline(_ time.Time) error      { return nil }
func (conn *portForwardConn) SetReadDeadline(_ time.Time) error  { return nil }
func (conn *portForwardConn) SetWriteDeadline(_ time.Time) error { return nil }

type portForwardAddr struct {
	podName string
	port    int
}

func (forwardAddr portForwardAddr) Network() string { return "spdy-portforward" }
func (forwardAddr portForwardAddr) String() string {
	return fmt.Sprintf("%s:%d", forwardAddr.podName, forwardAddr.port)
}
