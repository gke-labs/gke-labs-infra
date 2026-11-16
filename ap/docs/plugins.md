# Design: `ap` Plugin Contract (gRPC over Stdio)

## 1. Overview and Principles

`ap` automates development tasks across repositories using convention over configuration. Today, `ap` implements a fixed set of built-in toolchains (Go, protoc, docker, kind). To support additional toolchains without baking foreign runtimes (e.g. Node.js for Astro documentation sites) into the host or `ap` binary, `ap` introduces an extensible **plugin** model.

The design operates within the following established constraints (decisions 2026-11-15 & 2026-11-16):
- **gRPC over Stdio Transport**: `ap` launches the plugin container with stdin attached and no network, speaking gRPC directly over stdin and stdout. No TCP ports, mounted unix sockets, or `docker cp` commands are used.
- **End-to-End RPC Scope**: All operations—capability discovery (`Describe`), task execution (`RunTask`), source tree input streaming, task logs, and staged outputs—pass exclusively through the RPC stream. Container `/src` and `/out` directories are eliminated.
- **Declarative Activation & Directed Outputs**: Plugins declare repository-relative activation patterns (`activation: ["docs/astro.config.*"]`). The containing directory is the unit of work. Output files are received via RPC and written via `fileutils.UntrustedRoot` (regular files only, no `.git`, no path escapes).
- **Hermetic by Default & Two-Key Network**: Container runs with `--network none` unless network is declared in the plugin manifest AND explicitly authorized in `.ap/plugins.yaml`.
- **Unified Interface & Discovery Caching**: Built-in Go tooling and container plugins share the same plugin metadata abstractions. Discovery results are cached under `AP_CACHE_DIR` by image digest or local Dockerfile image ID.
- **Recommendation**: Standardize all container plugin interaction on gRPC over container stdin/stdout.
- **Alternative Not Taken**: Command-line subcommands (`describe`/`run`) combined with `docker cp` tar streaming into `/src` and `/out`. Rejected because multiple external `docker cp` invocations add process overhead, require temporary filesystem directories, and lack stream-level lifecycle control.

---

## 2. Plugin Service Definition (`ap.plugin/v1alpha1`)

Plugins communicate capabilities and execute tasks via a gRPC service:
```protobuf
syntax = "proto3";

package ap.plugin.v1alpha1;

option go_package = "github.com/gke-labs/gke-labs-infra/ap/pkg/plugins/api";

service PluginService {
  rpc Describe(DescribeRequest) returns (DescribeResponse);
  rpc RunTask(stream RunTaskRequest) returns (stream RunTaskResponse);
}

message DescribeRequest {}

message DescribeResponse {
  string name = 1;
  repeated string activation = 2;
  bool network = 3;
  repeated TaskMetadata tasks = 4;
}

message TaskMetadata {
  string name = 1;
  string verb = 2;
  string description = 3;
  repeated string outputs = 4;
}

message RunTaskRequest {
  oneof payload {
    TaskStart start = 1;
    FileChunk file = 2;
  }
}

message TaskStart {
  string task = 1;
  string dir = 2; // activated directory relative to repo root
}

message FileChunk {
  string path = 1;       // relative to repo root (input) or dir (output)
  uint32 mode = 2;       // POSIX file mode (e.g. 0644, 0755)
  bytes content = 3;     // chunk data (<= 1 MB)
  bool is_last_chunk = 4;
}

message RunTaskResponse {
  oneof payload {
    LogLine log = 1;
    FileChunk output_file = 2;
    TaskResult result = 3;
  }
}

message LogLine {
  enum Stream { STDOUT = 0; STDERR = 1; }
  Stream stream = 1;
  bytes data = 2;
}

message TaskResult {
  int32 exit_code = 1;
  string error_message = 2;
}
```

- **Relation to `SandboxService`**: `SandboxService` in `ap/pkg/sandbox/api/ap.proto` is an internal container execution service for running generic argv commands with per-file `WriteFile`/`ReadFile` RPCs. In contrast, `PluginService` defines an autonomous plugin domain model with capability discovery (`Describe`) and a single pipelined streaming RPC (`RunTask`) carrying files, logs, and outputs in one session. Reusing `SandboxService` would couple external plugins to internal execution semantics and force hundreds of sequential single-file RPCs instead of a single stream.
- **Versioning**: The contract is versioned as `ap.plugin/v1alpha1`. Because plugin support is unreleased and no external plugins exist, the gRPC protocol completely replaces the command-line contract without backward compatibility shims. Invocations of legacy CLI plugins fail immediately with an actionable error.
- **Recommendation**: Define `PluginService` as a dedicated gRPC service in `ap.plugin/v1alpha1`.
- **Alternative Not Taken**: Reusing or extending `SandboxService`, or introducing a dual-protocol `v1alpha2`. This would needlessly couple distinct lifecycle domains and require maintaining redundant execution paths.

---

## 3. Framing on Stdio & Transport

- **Duplex Stdio Connection**: gRPC runs over HTTP/2, requiring a bidirectional byte stream (`net.Conn`).
  - On the host, `ap` spawns `docker run -i --network none <image>` and wraps `cmd.StdinPipe()` and `cmd.StdoutPipe()` into a `net.Conn` duplex adapter passed to `grpc.WithContextDialer` (the same mechanism used by `buildctl dial-stdio` in Moby/BuildKit and HashiCorp `go-plugin`).
  - Inside the container, the plugin wraps `os.Stdin` and `os.Stdout` into a `net.Conn` and serves it with a `net.Listener` whose `Accept()` returns the connection once and blocks on subsequent calls.
- **Strict Stderr Rule**: Because container stdout carries binary HTTP/2 frames, any unstructured write to stdout corrupts the framing and breaks the connection. **Plugins MUST write all human-readable logs, debug messages, and diagnostic output to `stderr` only**. `ap` routes container stderr directly to host stderr/logging. Structured task logs can also be streamed via `LogLine` messages within `RunTaskResponse`.
- **Recommendation**: Run gRPC over container stdio using a BuildKit-style `dial-stdio` adapter, with all unstructured logging restricted to stderr.
- **Alternative Not Taken**: Unix domain sockets or localhost TCP. Bind-mounted unix sockets fail on macOS where Docker runs inside a VM (the socket appears in the filesystem but `connect(2)` fails across the hypervisor). TCP requires port allocation and risks port conflicts or firewall blocks.

---

## 4. Files In: Streaming vs. Bind Mount Evaluation

- **Baseline (RPC Streaming)**: `ap` streams the repository view as `FileChunk` messages over `RunTaskRequest`. `ap` populates this from `sourcetree.SourceTree`, which already filters out `.git/`, `node_modules/`, `.cache/`, and patterns in `.gitignore` and `.ap/` configs.
- **Evaluation of Read-Only Bind Mount (`-v <repoRoot>:/src:ro`)**:
  - *VM Compatibility*: Regular files are accessible across hypervisor-backed mounts (e.g. virtiofs on macOS Docker Desktop).
  - *Ignore Rule Enforcement*: A bind mount exposes the entire physical checkout to the container, including `.git/` (which may contain credentials or private hooks), untracked files, and ignored build caches. Enforcing `SourceTree` ignore rules would require dynamically creating dozens of empty mask mounts (`-v /empty:/src/.git:ro`), which is fragile, slow to start, and hits Docker mount count limits. Without masking, plugins can read uncommitted or private files, violating hermeticity.
  - *Large Tree Latency*: Filesystem virtualization across VM boundaries incurs significant metadata/stat overhead. Traversing a 10,000-file repository across virtiofs can take 2–5 seconds. Conversely, `ap` traverses the host tree once in memory (sub-millisecond) and streams a single linear payload into container memory or tmpfs.
- **Measured Numbers on this Repository**:
  - Checkout size: 420 tracked files, 2.16 MB total file content.
  - `SourceTree` load/cache: 3.3 ms.
  - RPC streaming (read and serialize all 420 files): ~16.6 ms. Total delivery time over stdio is under 30 ms.
  - Read-only bind mount setup takes ~0 ms on Linux, but reading 420 files across a VM boundary adds 100–300 ms of metadata sync overhead while leaking `.git` and untracked files.
- **Recommendation**: Stream the filtered `SourceTree` view over the RPC in `RunTaskRequest`.
- **Alternative Not Taken**: Read-only workspace bind mount. Rejected because it exposes sensitive `.git` contents, fails to honour ignore rules without complex volume masking, and suffers high metadata latency over VM boundaries.

---

## 5. Files Out & Staged Placement

- **Chunked Outputs**: Generated files are returned as `FileChunk` messages in `RunTaskResponse`:
  - Max chunk size: 1 MB per message (comfortably below gRPC's default 4 MB message limit).
  - Multi-chunk handling: Files exceeding 1 MB are split across multiple consecutive `FileChunk` messages with `is_last_chunk = false` until the final chunk.
  - Size Limits: `ap` enforces an upper limit (e.g. 100 MB per file) to prevent memory or disk exhaustion attacks.
- **Staging & Safety via `UntrustedRoot`**:
  - Received chunks are written to a temporary host staging directory using `fileutils.UntrustedRoot`.
  - `UntrustedRoot` verifies that files are regular files (no FIFOs, device nodes, or symlink traversal attacks) and ensures no files escape the target directory or touch `.git`.
  - In execution mode, staged files are atomically copied into the workspace. In verify mode (`ap generate --verify`), staged files are compared byte-for-byte against the workspace, printing a diff and failing if outputs do not match.
- **Recommendation**: Stream output files in 1 MB chunks over `RunTaskResponse` and stage on the host via `fileutils.UntrustedRoot`.
- **Alternative Not Taken**: Writing outputs to a container `/out` volume and retrieving via `docker cp` or a secondary tar stream. In-band RPC streaming unifies status, logging, and outputs in a single atomic lifecycle.

---

## 6. Configuration & Multi-Plugin Orchestration

- **Configuration**: Plugins are declared in `.ap/plugins.yaml` (or `ap.yaml` under `plugins:`):
  ```yaml
  plugins:
    astro:
      image: ghcr.io/gke-labs/astro-builder@sha256:4b22...
      network: false
    local-tool:
      dockerfile: images/local-builder/Dockerfile
      network: false
  ```
- **Two-Key Network Authorization**: Both the manifest (`network: true`) and `.ap/plugins.yaml` (`network: true`) must agree; otherwise, `ap` refuses execution.
- **Discovery Caching**: On cold discovery, `ap` runs `Describe` over stdio and caches the manifest under `${AP_CACHE_DIR:-$HOME/.cache/ap}/plugins/describe/<digest-or-id>.json`. Warm commands (`ap ls`, `ap test`) read cached JSON with zero container invocations.
- **Task Naming & Conflicts**: Plugin tasks use hyphenated names (`<plugin>-<task>`, e.g. `astro-build`). Multiple plugins in the same directory execute in deterministic alphabetical order. Overlapping task output paths are detected during discovery and fail fast.
- **Recommendation**: Maintain two-key network authorization, discovery caching under `AP_CACHE_DIR`, and hyphenated task naming.
- **Alternative Not Taken**: Dynamic container discovery on every run, or static image labels that restrict dynamic task declarations.

---

## 7. Smallest Plugin in Go (Author Experience)

The minimal plugin in Go demonstrates that speaking gRPC over stdio requires under 50 lines of code:
```go
package main

import (
	"context"
	"io"
	"os"

	"google.golang.org/grpc"
	pb "github.com/gke-labs/gke-labs-infra/ap/pkg/plugins/api"
)

type server struct{ pb.UnimplementedPluginServiceServer }

func (s *server) Describe(ctx context.Context, _ *pb.DescribeRequest) (*pb.DescribeResponse, error) {
	return &pb.DescribeResponse{
		Name: "example", Activation: []string{"example.conf"},
		Tasks: []*pb.TaskMetadata{{Name: "build", Verb: "build", Outputs: []string{"dist"}}},
	}, nil
}

func (s *server) RunTask(stream pb.PluginService_RunTaskServer) error {
	for {
		req, err := stream.Recv()
		if err == io.EOF { break }
		if err != nil { return err }
		_ = req // process start config and incoming file chunks
	}
	// Execute task logic; send logs to os.Stderr
	return stream.Send(&pb.RunTaskResponse{
		Payload: &pb.RunTaskResponse_Result{Result: &pb.TaskResult{ExitCode: 0}},
	})
}

func main() {
	srv := grpc.NewServer()
	pb.RegisterPluginServiceServer(srv, &server{})
	srv.Serve(newStdioListener(os.Stdin, os.Stdout)) // 15-line helper: yields stdio conn once
}
```
- **Recommendation**: Provide a standard `ServeStdio` helper in `ap/pkg/plugins/sdk` while keeping the wire contract pure gRPC.
- **Alternative Not Taken**: Custom JSON-RPC or text protocols. gRPC provides typed schemas, cross-language stubs, streaming, and built-in error propagation.

---

## 8. Implementation Sequence

The migration to gRPC over stdio is structured into incremental PRs of approximately 200 lines each:

1. **#816 Design Update** (This PR): Architecture and contract specification in `ap/docs/plugins.md`.
2. **Plugin Service Proto & Stubs**: Define `ap/pkg/plugins/api/plugin.proto` and generate Go protobuf/gRPC stubs (~150 lines).
3. **Stdio Duplex Transport**: Implement `net.Conn` stdio adapter and single-connection listener (`dial-stdio`) (~200 lines).
4. **Container Runner Migration**: Update `ContainerPlugin.Describe` and `ContainerTask.Run` to speak gRPC over stdio, streaming input files and receiving output chunks via `fileutils.UntrustedRoot` (~250 lines).
5. **Plugin Go SDK & Test Harness**: Provide `ServeStdio` helper and update plugin test mocks to gRPC stdio (~200 lines).
6. **Container Verify Mode**: Integrate staging diff comparison for `generate`-verb tasks with the gRPC streaming runner (~200 lines).
7. **Astro Plugin (`ation`)**: Import Astro builder image using the gRPC stdio runner (~200 lines).

- **Recommendation**: Incrementally land proto, transport, and runner in ~200-line PRs, replacing `docker cp` entirely.
- **Alternative Not Taken**: Maintaining dual CLI and gRPC runners during transition. Dual runners double testing complexity for zero active users.
