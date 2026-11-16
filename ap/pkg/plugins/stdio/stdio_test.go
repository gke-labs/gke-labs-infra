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

package stdio

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

const envTestMode = "AP_STDIO_TEST_MODE"

func TestMain(testRunner *testing.M) {
	switch os.Getenv(envTestMode) {
	case "server":
		runTestServer()
		os.Exit(0)
	case "early_exit":
		os.Exit(42)
	case "ignore_stdin":
		time.Sleep(8 * time.Second)
		os.Exit(0)
	default:
		os.Exit(testRunner.Run())
	}
}

func runTestServer() {
	server := grpc.NewServer()
	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(server, healthServer)
	healthServer.SetServingStatus("test", healthpb.HealthCheckResponse_SERVING)
	go func() {
		time.Sleep(50 * time.Millisecond)
		healthServer.SetServingStatus("test", healthpb.HealthCheckResponse_NOT_SERVING)
	}()
	listener := NewListener(os.Stdin, os.Stdout)
	if err := server.Serve(listener); err != nil && !errors.Is(err, net.ErrClosed) {
		os.Exit(1)
	}
}

func startChild(t *testing.T, mode string) *CommandConn {
	t.Helper()
	executablePath, err := os.Executable()
	if err != nil {
		executablePath = os.Args[0]
	}
	command := exec.Command(executablePath)
	command.Env = append(os.Environ(), envTestMode+"="+mode)
	stdinPipe, err := command.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdoutPipe, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return NewCommandConn(command, stdinPipe, stdoutPipe)
}

func testRoundTrip(t *testing.T, connection net.Conn, onInitialStream func()) {
	t.Helper()
	clientConnection, err := grpc.NewClient("passthrough:///stdio",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return connection, nil
		}),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer func() {
		if err := clientConnection.Close(); err != nil {
			t.Errorf("clientConnection.Close: %v", err)
		}
	}()

	client := healthpb.NewHealthClient(clientConnection)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	response, err := client.Check(ctx, &healthpb.HealthCheckRequest{Service: "test"})
	if err != nil {
		t.Fatalf("client.Check: %v", err)
	}
	if response.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("expected SERVING, got %v", response.Status)
	}

	stream, err := client.Watch(ctx, &healthpb.HealthCheckRequest{Service: "test"})
	if err != nil {
		t.Fatalf("client.Watch: %v", err)
	}
	initialResponse, err := stream.Recv()
	if err != nil {
		t.Fatalf("stream.Recv initial: %v", err)
	}
	if initialResponse.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("expected initial SERVING, got %v", initialResponse.Status)
	}

	if onInitialStream != nil {
		onInitialStream()
	}

	updatedResponse, err := stream.Recv()
	if err != nil {
		t.Fatalf("stream.Recv update: %v", err)
	}
	if updatedResponse.Status != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("expected updated NOT_SERVING, got %v", updatedResponse.Status)
	}
}

func TestRoundTrip_InProcess(t *testing.T) {
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()

	serverListener := NewListener(serverReader, serverWriter)
	clientConnection := NewConn(clientReader, clientWriter)

	server := grpc.NewServer()
	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(server, healthServer)
	healthServer.SetServingStatus("test", healthpb.HealthCheckResponse_SERVING)

	serverErrorChannel := make(chan error, 1)
	go func() {
		serverErrorChannel <- server.Serve(serverListener)
	}()

	testRoundTrip(t, clientConnection, func() {
		healthServer.SetServingStatus("test", healthpb.HealthCheckResponse_NOT_SERVING)
	})

	server.GracefulStop()
	if err := <-serverErrorChannel; err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("server.Serve error: %v", err)
	}
}

func TestRoundTrip_ChildProcess(t *testing.T) {
	connection := startChild(t, "server")
	testRoundTrip(t, connection, nil)
	if err := connection.Close(); err != nil {
		t.Fatalf("connection.Close: %v", err)
	}
}

func TestChildProcess_EarlyExit(t *testing.T) {
	connection := startChild(t, "early_exit")
	defer func() {
		_ = connection.Close()
	}()

	clientConnection, err := grpc.NewClient("passthrough:///stdio",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return connection, nil
		}),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer func() {
		_ = clientConnection.Close()
	}()

	client := healthpb.NewHealthClient(clientConnection)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	startTime := time.Now()
	_, err = client.Check(ctx, &healthpb.HealthCheckRequest{Service: "test"})
	elapsed := time.Since(startTime)
	if err == nil {
		t.Fatal("expected error from early exit child, got nil")
	}
	if !strings.Contains(err.Error(), "42") {
		t.Fatalf("expected error to contain exit status 42, got: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("expected bounded time error, took: %v", elapsed)
	}

	closeError := connection.Close()
	if closeError == nil || !strings.Contains(closeError.Error(), "42") {
		t.Fatalf("expected Close() to return exit status 42, got: %v", closeError)
	}
}

func TestChildProcess_IgnoresStdinClose(t *testing.T) {
	connection := startChild(t, "ignore_stdin")

	startTime := time.Now()
	err := connection.Close()
	elapsed := time.Since(startTime)

	if err == nil {
		t.Fatal("expected Close to return error when child process was killed, got nil")
	}
	if !strings.Contains(err.Error(), "killed") {
		t.Fatalf("expected error to mention child was killed, got: %v", err)
	}
	if elapsed < 1500*time.Millisecond {
		t.Fatalf("Close returned too quickly (%v), expected to wait grace period", elapsed)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("Close took too long (%v), expected bounded wait (< 4s)", elapsed)
	}
}

func TestDeadlines(t *testing.T) {
	pipeReader, pipeWriter := io.Pipe()
	defer func() {
		_ = pipeReader.Close()
		_ = pipeWriter.Close()
	}()

	testCommand := exec.Command("true")
	commandStdin, err := testCommand.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	commandStdout, err := testCommand.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := testCommand.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	commandConnection := NewCommandConn(testCommand, commandStdin, commandStdout)
	defer func() {
		_ = commandConnection.Close()
	}()

	now := time.Now()
	for name, connection := range map[string]net.Conn{
		"Conn":        NewConn(pipeReader, pipeWriter),
		"CommandConn": commandConnection,
	} {
		if !errors.Is(connection.SetDeadline(now), ErrDeadlinesNotSupported) ||
			!errors.Is(connection.SetReadDeadline(now), ErrDeadlinesNotSupported) ||
			!errors.Is(connection.SetWriteDeadline(now), ErrDeadlinesNotSupported) {
			t.Errorf("%s: expected ErrDeadlinesNotSupported", name)
		}
	}
}
