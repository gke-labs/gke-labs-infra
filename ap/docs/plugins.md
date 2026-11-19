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

- **File Transmission Limits & Semantics**: `FileChunk` carries only regular file content.
  - *Symlinks*: Symlinks cannot be represented directly as symlinks in `FileChunk`. Resolution happens through an `os.Root` on the repository, as output placement does. A symlink whose target resolves to a file inside the repository view is sent as that file's content; a symlink that resolves outside the view, or to a path the view ignores, or that is dangling, is not followed, and `ap` fails the task with an error naming the link. Output files produced by the plugin cannot be symlinks (attempted symlinks in output streams are rejected by `fileutils.UntrustedRoot`).
  - *Empty Directories*: Empty directories are not transmitted; directories are created implicitly on the receiving side from file path hierarchies.
  - *File Modes*: When writing output files, `ap` applies only the permission bits of `mode` (`os.FileMode(mode).Perm()`), preserving executable flags (`0755` vs `0644`) while discarding setuid/setgid/sticky bits.
- **Two Failure Modes**:
  - *Task execution failure (`TaskResult` with non-zero `exit_code`)*: The task ran inside the plugin and completed with an error status (e.g. test failure or linter issue). `ap` records the exit status, flushes any streamed `LogLine` output into the task log, and prints the standard task failure line (e.g. `=== astro-build docs (failed, 1.4s)`).
  - *gRPC status error*: An infrastructure, protocol, or plugin crash (e.g. unknown task, invalid chunk framing, unhandled panic, or stream disconnection). `ap` treats this as a plugin fault, prints the gRPC status code and error message along with any container stderr, and aborts execution with an error.
- **Relation to `SandboxService`**: `SandboxService` in `ap/pkg/sandbox/api/ap.proto` is an internal container execution service for running generic argv commands with per-file `WriteFile`/`ReadFile` RPCs. In contrast, `PluginService` defines an autonomous plugin domain model with capability discovery (`Describe`) and a single pipelined streaming RPC (`RunTask`) carrying files, logs, and outputs in one session. Reusing `SandboxService` would couple external plugins to internal execution semantics and force hundreds of sequential single-file RPCs instead of a single stream.
- **Versioning**: The contract is versioned as `ap.plugin/v1alpha1`. Because plugin support is unreleased and no external plugins exist, the gRPC protocol completely replaces the command-line contract without backward compatibility shims. Invocations of legacy CLI plugins fail immediately with an actionable error.
- **Recommendation**: Define `PluginService` as a dedicated gRPC service in `ap.plugin/v1alpha1`.
- **Alternative Not Taken**: Reusing or extending `SandboxService`, or introducing a dual-protocol `v1alpha2`. This would needlessly couple distinct lifecycle domains and require maintaining redundant execution paths.

---

## 3. Framing on Stdio & Transport

- **Duplex Stdio Connection**: gRPC runs over HTTP/2, requiring a bidirectional byte stream (`net.Conn`).
  - On the host, `ap` spawns `docker run -i --network none <image>` and wraps `cmd.StdinPipe()` and `cmd.StdoutPipe()` into a `net.Conn` duplex adapter passed to `grpc.WithContextDialer` (the same command-as-connection mechanism used by BuildKit's connection helpers in `github.com/docker/cli/cli/connhelper/commandconn` and `buildctl dial-stdio`).
  - Inside the container, the plugin wraps `os.Stdin` and `os.Stdout` into a `net.Conn` and serves it with a `net.Listener` whose `Accept()` returns the connection once and blocks on subsequent calls.
- **Logging Roles (Two Channels)**:
  - *Task tool output (`LogLine`)*: Output produced by the tools that a task executes travels strictly as `LogLine` messages over the `RunTaskResponse` stream. This allows `ap` to interleave stdout and stderr in chronological order within the task log and route output to `TaskContext.Stdout()` and `TaskContext.Stderr()`.
  - *Container stderr*: The container's raw `stderr` is reserved exclusively for the plugin process's own internal diagnostics when no task stream exists (e.g. startup initialization failures or fatal crashes before gRPC serves). `ap` writes container stderr to its verbose debug log (`scoped.Log(ctx).Fine`), or prints it to host stderr if the container exits unexpectedly. Container stdout carries binary HTTP/2 frames and must never receive unstructured text.
- **Recommendation**: Run gRPC over container stdio using a BuildKit-style `dial-stdio` adapter, routing task output to `LogLine` and plugin process diagnostics to stderr.
- **Alternative Not Taken**: Unix domain sockets or localhost TCP. Bind-mounted unix sockets fail on macOS where Docker runs inside a VM (the socket appears in the filesystem but `connect(2)` fails across the hypervisor). TCP requires port allocation and risks port conflicts or firewall blocks.

---

## 4. Files In: Streaming vs. Bind Mount Evaluation

- **Baseline (RPC Streaming)**: `ap` streams the repository view as `FileChunk` messages over `RunTaskRequest`. `ap` populates this from `sourcetree.SourceTree`, which already filters out `.git/`, `node_modules/`, `.cache/`, and patterns in `.gitignore` and `.ap/` configs.
- **Evaluation of Read-Only Bind Mount (`-v <repoRoot>:/src:ro`)**:
  - *VM Compatibility*: Regular files are accessible across hypervisor-backed mounts (e.g. virtiofs / reverse sshfs on macOS Docker Desktop / lima).
  - *Ignore Rule Enforcement*: A bind mount exposes the entire physical checkout to the container, including `.git/` (which was confirmed visible in VM bind-mount tests and may contain credentials or private hooks), untracked files, and ignored build caches. Enforcing `SourceTree` ignore rules would require dynamically creating dozens of empty mask mounts (`-v /empty:/src/.git:ro`), which is fragile, slow to start, and hits Docker mount count limits. Without masking, plugins can read uncommitted or private files, violating hermeticity.
- **Measured Figures & Latency**:
  - *Host baseline*: 420 tracked files (2.27 MB total content). `SourceTree` load takes ~3.3 ms; reading and serializing all 420 files takes ~16.6 ms.
  - *VM bind-mount benchmark (measured on macOS with Docker in a lima VM)*: Container startup takes ~0.25 s; reading 280 files through a read-only bind mount of a checkout took 0.23 s (compared to 0.06 s for 420 files on host native storage), and `.git` was visible inside the mount.
- **Cost of the Recommendation & When to Revisit**:
  - Streaming the whole repository view happens for every task run and scales with repository size. Furthermore, because external tools inside the container require filesystem paths, the plugin must unpack and write incoming `FileChunk` messages to its local filesystem before tool execution can begin.
  - If repositories grow to sizes where per-task streaming overhead becomes prohibitive, the maintainer's "faster way" (a read-only bind mount) should be revisited—specifically when container sandboxing can reliably mask `.git` and enforce ignore rules without VM virtualization penalties.
- **Recommendation**: Stream the filtered `SourceTree` view over the RPC in `RunTaskRequest`.
- **Alternative Not Taken**: Read-only workspace bind mount. Rejected because it exposes sensitive `.git` contents, fails to honour ignore rules without complex volume masking, and incurs metadata overhead over VM boundaries.

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

- **Configuration**: Plugins are declared in `.ap/plugins.yaml`:
  ```yaml
  plugins:
    astro:
      image: ghcr.io/gke-labs/astro-builder@sha256:4b22...
      network: false
    remote-tool:
      dockerfile: https://github.com/gke-labs/gke-labs-infra/blob/<commit>/ap/images/ap-plugin-astro/Dockerfile
      network: false
    local-tool:
      dockerfile: images/local-builder/Dockerfile
      network: false
  ```
- **Two-Key Network Authorization**: Both the manifest (`network: true`) and `.ap/plugins.yaml` (`network: true`) must agree; otherwise, `ap` refuses execution.
- **Discovery Caching**: On cold discovery, `ap` runs `Describe` over stdio and caches the manifest under `${AP_CACHE_DIR:-$HOME/.cache/ap}/plugins/describe/<digest-or-id>.json`. Warm commands (`ap ls`, `ap test`) read cached JSON with zero container invocations.
- **Task Naming & Conflicts**: Plugin tasks use hyphenated names (`<plugin>-<task>`, e.g. `astro-build`). Multiple plugins in the same directory execute in deterministic alphabetical order. Overlapping task output path detection during discovery is planned.
- **Entrypoint Binary**: By convention, a plugin's entrypoint binary is named `ap-plugin-<plugin name>`, and a plugin kept in this repository lives in `ap/images/ap-plugin-<plugin name>`.
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
	// Execute task logic; stream tool logs as LogLine messages
	_ = stream.Send(&pb.RunTaskResponse{
		Payload: &pb.RunTaskResponse_Log{
			Log: &pb.LogLine{Stream: pb.LogLine_STDOUT, Data: []byte("built dist/\n")},
		},
	})
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

1. **#816 Design Update**: Architecture and contract specification in `ap/docs/plugins.md`.
2. **Plugin Service Proto & Stubs**: Define `ap/pkg/plugins/api/plugin.proto` and generate Go protobuf/gRPC stubs.
3. **Stdio Duplex Transport**: Implement `net.Conn` stdio adapter and single-connection listener (`dial-stdio`).
4. **Container Runner Migration**: Update `ContainerPlugin.Describe` and `ContainerTask.Run` to speak gRPC over stdio, streaming input files and receiving output chunks via `fileutils.UntrustedRoot`.
5. **Plugin Go SDK & Test Harness**: Provide `ServeStdio` helper and update plugin test mocks to gRPC stdio.
6. **Container Verify Mode**: Integrate staging diff comparison for `generate`-verb tasks with the gRPC streaming runner.
7. **Astro Plugin (`ation`)**: Import Astro builder image using the gRPC stdio runner.

- **Recommendation**: Incrementally land proto, transport, and runner in ~200-line PRs, replacing `docker cp` entirely.
- **Alternative Not Taken**: Maintaining dual CLI and gRPC runners during transition. Dual runners double testing complexity for zero active users.
