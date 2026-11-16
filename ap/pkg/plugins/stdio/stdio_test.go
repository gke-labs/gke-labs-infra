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

func TestMain(m *testing.M) {
	switch os.Getenv(envTestMode) {
	case "server":
		runTestServer()
		os.Exit(0)
	case "early_exit":
		os.Exit(42)
	default:
		os.Exit(m.Run())
	}
}

func runTestServer() {
	srv := grpc.NewServer()
	h := health.NewServer()
	healthpb.RegisterHealthServer(srv, h)
	h.SetServingStatus("test", healthpb.HealthCheckResponse_SERVING)
	go func() {
		time.Sleep(50 * time.Millisecond)
		h.SetServingStatus("test", healthpb.HealthCheckResponse_NOT_SERVING)
	}()
	lis := NewListener(os.Stdin, os.Stdout)
	if err := srv.Serve(lis); err != nil && !errors.Is(err, net.ErrClosed) {
		os.Exit(1)
	}
}

func startChild(t *testing.T, mode string) *CommandConn {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		bin = os.Args[0]
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), envTestMode+"="+mode)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return NewCommandConn(cmd, in, out)
}

func testRoundTrip(t *testing.T, conn net.Conn, onInitialStream func()) {
	t.Helper()
	cc, err := grpc.NewClient("passthrough:///stdio",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return conn, nil
		}),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer func() {
		if err := cc.Close(); err != nil {
			t.Errorf("cc.Close: %v", err)
		}
	}()

	client := healthpb.NewHealthClient(cc)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	resp, err := client.Check(ctx, &healthpb.HealthCheckRequest{Service: "test"})
	if err != nil {
		t.Fatalf("client.Check: %v", err)
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("expected SERVING, got %v", resp.Status)
	}

	stream, err := client.Watch(ctx, &healthpb.HealthCheckRequest{Service: "test"})
	if err != nil {
		t.Fatalf("client.Watch: %v", err)
	}
	sResp, err := stream.Recv()
	if err != nil {
		t.Fatalf("stream.Recv initial: %v", err)
	}
	if sResp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("expected initial SERVING, got %v", sResp.Status)
	}

	if onInitialStream != nil {
		onInitialStream()
	}

	sResp2, err := stream.Recv()
	if err != nil {
		t.Fatalf("stream.Recv update: %v", err)
	}
	if sResp2.Status != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("expected updated NOT_SERVING, got %v", sResp2.Status)
	}
}

func TestRoundTrip_InProcess(t *testing.T) {
	serverR, clientW := io.Pipe()
	clientR, serverW := io.Pipe()

	serverLis := NewListener(serverR, serverW)
	clientConn := NewConn(clientR, clientW)

	srv := grpc.NewServer()
	h := health.NewServer()
	healthpb.RegisterHealthServer(srv, h)
	h.SetServingStatus("test", healthpb.HealthCheckResponse_SERVING)

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.Serve(serverLis)
	}()

	testRoundTrip(t, clientConn, func() {
		h.SetServingStatus("test", healthpb.HealthCheckResponse_NOT_SERVING)
	})

	srv.GracefulStop()
	if err := <-serverErr; err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("srv.Serve error: %v", err)
	}
}

func TestRoundTrip_ChildProcess(t *testing.T) {
	conn := startChild(t, "server")
	testRoundTrip(t, conn, nil)
	if err := conn.Close(); err != nil {
		t.Fatalf("conn.Close: %v", err)
	}
}

func TestChildProcess_EarlyExit(t *testing.T) {
	conn := startChild(t, "early_exit")
	defer func() {
		_ = conn.Close()
	}()

	cc, err := grpc.NewClient("passthrough:///stdio",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return conn, nil
		}),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer func() {
		_ = cc.Close()
	}()

	client := healthpb.NewHealthClient(cc)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	t0 := time.Now()
	_, err = client.Check(ctx, &healthpb.HealthCheckRequest{Service: "test"})
	elapsed := time.Since(t0)
	if err == nil {
		t.Fatal("expected error from early exit child, got nil")
	}
	if !strings.Contains(err.Error(), "42") {
		t.Fatalf("expected error to contain exit status 42, got: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("expected bounded time error, took: %v", elapsed)
	}

	closeErr := conn.Close()
	if closeErr == nil || !strings.Contains(closeErr.Error(), "42") {
		t.Fatalf("expected Close() to return exit status 42, got: %v", closeErr)
	}
}

func TestDeadlines(t *testing.T) {
	now := time.Now()
	for name, c := range map[string]net.Conn{
		"Conn":        NewConn(nil, nil),
		"CommandConn": NewCommandConn(exec.Command("true"), nil, nil),
	} {
		if !errors.Is(c.SetDeadline(now), ErrDeadlinesNotSupported) ||
			!errors.Is(c.SetReadDeadline(now), ErrDeadlinesNotSupported) ||
			!errors.Is(c.SetWriteDeadline(now), ErrDeadlinesNotSupported) {
			t.Errorf("%s: expected ErrDeadlinesNotSupported", name)
		}
	}
}
