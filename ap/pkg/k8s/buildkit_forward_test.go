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
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/tools/portforward"
)

type fakeStream struct {
	reader  io.Reader
	writer  io.Writer
	closer  io.Closer
	headers http.Header
}

func (s *fakeStream) Read(p []byte) (int, error)  { return s.reader.Read(p) }
func (s *fakeStream) Write(p []byte) (int, error) { return s.writer.Write(p) }
func (s *fakeStream) Close() error {
	if s.closer != nil {
		return s.closer.Close()
	}
	return nil
}
func (s *fakeStream) Reset() error         { return s.Close() }
func (s *fakeStream) Headers() http.Header { return s.headers }
func (s *fakeStream) Identifier() uint32   { return 1 }

type fakeConnection struct {
	dataStream  httpstream.Stream
	errorStream httpstream.Stream
	closeChan   chan bool
}

func (c *fakeConnection) CreateStream(headers http.Header) (httpstream.Stream, error) {
	if headers.Get(corev1.StreamType) == corev1.StreamTypeError {
		return c.errorStream, nil
	}
	return c.dataStream, nil
}
func (c *fakeConnection) Close() error                         { return nil }
func (c *fakeConnection) CloseChan() <-chan bool               { return c.closeChan }
func (c *fakeConnection) SetIdleTimeout(_ time.Duration)       {}
func (c *fakeConnection) RemoveStreams(_ ...httpstream.Stream) {}

type fakeDialer struct {
	conn httpstream.Connection
}

func (d *fakeDialer) Dial(_ ...string) (httpstream.Connection, string, error) {
	return d.conn, portforward.PortForwardProtocolV1Name, nil
}

func TestBuildkitForwarder(t *testing.T) {
	testReadPipe, forwarderWritePipe := io.Pipe()
	forwarderReadPipe, testWritePipe := io.Pipe()

	fakeDataStream := &fakeStream{
		reader:  forwarderReadPipe,
		writer:  forwarderWritePipe,
		closer:  forwarderWritePipe,
		headers: http.Header{},
	}
	fakeErrorStream := &fakeStream{
		reader:  bytes.NewReader(nil),
		headers: http.Header{},
	}

	dialer := &fakeDialer{
		conn: &fakeConnection{
			dataStream:  fakeDataStream,
			errorStream: fakeErrorStream,
			closeChan:   make(chan bool),
		},
	}

	forwarder, err := newBuildkitForwarderWithDialer(t.Context(), dialer, 2375)
	if err != nil {
		t.Fatalf("newBuildkitForwarderWithDialer failed: %v", err)
	}
	defer func() {
		if err := forwarder.Close(); err != nil {
			t.Errorf("forwarder.Close failed: %v", err)
		}
	}()

	socketDirectory := forwarder.socketDirectory
	info, err := os.Stat(socketDirectory)
	if err != nil {
		t.Fatalf("failed to stat socket directory: %v", err)
	}
	if info.Mode().Perm() != 0700 {
		t.Errorf("expected socket directory mode 0700, got %04o", info.Mode().Perm())
	}

	clientConn, err := net.Dial("unix", forwarder.SocketPath())
	if err != nil {
		t.Fatalf("failed to dial unix socket: %v", err)
	}
	defer clientConn.Close()

	wantRequest := []byte("hello buildkit")
	if _, err := clientConn.Write(wantRequest); err != nil {
		t.Fatalf("client write failed: %v", err)
	}

	receivedRequest := make([]byte, len(wantRequest))
	if _, err := io.ReadFull(testReadPipe, receivedRequest); err != nil {
		t.Fatalf("failed reading from test pipe: %v", err)
	}
	if !bytes.Equal(receivedRequest, wantRequest) {
		t.Fatalf("got %q, want %q", receivedRequest, wantRequest)
	}

	wantReply := []byte("hello client")
	if _, err := testWritePipe.Write(wantReply); err != nil {
		t.Fatalf("reply write failed: %v", err)
	}

	receivedReply := make([]byte, len(wantReply))
	if _, err := io.ReadFull(clientConn, receivedReply); err != nil {
		t.Fatalf("failed reading from client socket: %v", err)
	}
	if !bytes.Equal(receivedReply, wantReply) {
		t.Fatalf("got %q, want %q", receivedReply, wantReply)
	}

	if err := clientConn.Close(); err != nil {
		t.Fatalf("client close failed: %v", err)
	}
	if err := testWritePipe.Close(); err != nil {
		t.Fatalf("test write pipe close failed: %v", err)
	}
	if err := forwarder.Close(); err != nil {
		t.Fatalf("forwarder close failed: %v", err)
	}

	if _, err := os.Stat(socketDirectory); !os.IsNotExist(err) {
		t.Errorf("expected socket directory %s to be removed, got err: %v", socketDirectory, err)
	}
}
