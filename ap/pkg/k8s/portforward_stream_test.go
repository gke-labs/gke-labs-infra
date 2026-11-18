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
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
)

func TestPodPortForwarder_DialStream(t *testing.T) {
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

	forwarder, err := NewPodPortForwarderWithDialer(scoped.Log(t.Context()), dialer, "test-pod-0", 5000)
	if err != nil {
		t.Fatalf("NewPodPortForwarderWithDialer failed: %v", err)
	}
	defer func() {
		if closeErr := forwarder.Close(); closeErr != nil {
			t.Errorf("forwarder.Close failed: %v", closeErr)
		}
	}()

	if forwarder.PodName() != "test-pod-0" {
		t.Errorf("expected pod name %q, got %q", "test-pod-0", forwarder.PodName())
	}
	if forwarder.RemotePort() != 5000 {
		t.Errorf("expected remote port 5000, got %d", forwarder.RemotePort())
	}

	conn, err := forwarder.DialStream()
	if err != nil {
		t.Fatalf("DialStream failed: %v", err)
	}
	defer conn.Close()

	wantRequest := []byte("ping stream")
	readDone := make(chan []byte, 1)
	go func() {
		receivedRequest := make([]byte, len(wantRequest))
		_, _ = io.ReadFull(testReadPipe, receivedRequest)
		readDone <- receivedRequest
	}()

	if _, err := conn.Write(wantRequest); err != nil {
		t.Fatalf("conn.Write failed: %v", err)
	}

	receivedRequest := <-readDone
	if !bytes.Equal(receivedRequest, wantRequest) {
		t.Fatalf("got %q, want %q", receivedRequest, wantRequest)
	}

	wantReply := []byte("pong stream")
	replyDone := make(chan []byte, 1)
	go func() {
		receivedReply := make([]byte, len(wantReply))
		_, _ = io.ReadFull(conn, receivedReply)
		replyDone <- receivedReply
	}()

	if _, err := testWritePipe.Write(wantReply); err != nil {
		t.Fatalf("testWritePipe.Write failed: %v", err)
	}

	receivedReply := <-replyDone
	if !bytes.Equal(receivedReply, wantReply) {
		t.Fatalf("got %q, want %q", receivedReply, wantReply)
	}
}

func TestPodPortForwarder_StreamError(t *testing.T) {
	fakeDataStream := &fakeStream{
		reader:  bytes.NewReader(nil),
		writer:  io.Discard,
		headers: http.Header{},
	}
	fakeErrorStream := &fakeStream{
		reader:  strings.NewReader("dial tcp 127.0.0.1:5000: connect: connection refused\n"),
		headers: http.Header{},
	}

	dialer := &fakeDialer{
		conn: &fakeConnection{
			dataStream:  fakeDataStream,
			errorStream: fakeErrorStream,
			closeChan:   make(chan bool),
		},
	}

	forwarder, err := NewPodPortForwarderWithDialer(scoped.Log(t.Context()), dialer, "test-pod-0", 5000)
	if err != nil {
		t.Fatalf("NewPodPortForwarderWithDialer failed: %v", err)
	}
	defer forwarder.Close()

	conn, err := forwarder.DialStream()
	if err != nil {
		t.Fatalf("DialStream failed: %v", err)
	}

	buffer := make([]byte, 10)
	_, readErr := conn.Read(buffer)
	if !errors.Is(readErr, io.EOF) {
		t.Errorf("expected io.EOF on data stream, got %v", readErr)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("conn.Close failed: %v", err)
	}
}

func TestPodPortForwarder_CloseFailsBlockedRead(t *testing.T) {
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

	forwarder, err := NewPodPortForwarderWithDialer(scoped.Log(t.Context()), dialer, "test-pod-0", 5000)
	if err != nil {
		t.Fatalf("NewPodPortForwarderWithDialer failed: %v", err)
	}

	conn, err := forwarder.DialStream()
	if err != nil {
		t.Fatalf("DialStream failed: %v", err)
	}
	defer conn.Close()

	readErrChan := make(chan error, 1)
	go func() {
		buffer := make([]byte, 10)
		_, readErr := conn.Read(buffer)
		readErrChan <- readErr
	}()

	_ = testWritePipe.Close()
	_ = forwarder.Close()
	_ = testReadPipe.Close()

	select {
	case readErr := <-readErrChan:
		if readErr == nil {
			t.Fatal("expected error on closed forwarder read, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked read did not fail promptly on close")
	}
}
