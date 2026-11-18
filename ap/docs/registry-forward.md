<!--
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Investigation: Removing the Registry Port-Forward on localhost:5000

## Executive Summary & Background

`ap` adheres to a strict security and operational posture: **avoid opening local TCP listeners whenever possible**. Even when bound to `localhost` or `127.0.0.1`, local TCP listeners introduce concrete risks:
1. **Local Access & Cross-Origin Exposure**: Any unprivileged process, background service, or browser tab running on the developer workstation (via loopback WebSockets or cross-origin fetch requests) can connect to open localhost ports.
2. **Port Collisions & Flakiness**: Fixed ports (such as `5000`) frequently collide with existing services. On macOS Monterey (12.0) and later, the system AirPlay Receiver feature (`ControlCenter`) binds TCP port `*:5000` by default across all interfaces (verifiable via `lsof -i :5000` or System Settings -> General -> AirDrop & Handoff -> AirPlay Receiver, as noted in `ap/pkg/k8s/portforward.go:134`). Multiple concurrent `ap` invocations or local container registries also fail with `bind: address already in use`.
3. **Cross-Platform Complexity**: On macOS, Docker runs inside a virtualized Linux VM. A port-forward bound to the macOS host loopback is unreachable from inside the VM, forcing `ap` to maintain a secondary `alpine/socat` bridge container (`ap-registry-proxy`) that publishes ports inside the VM and routes back to the host via `host.docker.internal`.

`ap` previously resolved this problem for the BuildKit daemon connection (Issue #797). The BuildKit bridge moved from a local TCP port-forward to an in-process forwarder over a private Unix domain socket located in a mode `0700` temporary directory (`ap/pkg/k8s/buildkit_forward.go`).

Currently, one TCP listener remains in `ap`: when container images are pushed to the in-cluster registry (`in-cluster-image-registry`), `ap` forwards the registry service to `localhost:5000` (`ap/pkg/images/build.go`, `ap/pkg/k8s/portforward.go`, `ap/pkg/imageref/naming.go`).

This report investigates the necessity of this remaining port-forward, tests potential non-listener alternatives, evaluates hardening strategies if a listener were retained, and provides concrete recommendations for eliminating it.

---

## 1. Current Build Paths and Programs Talking to `localhost:5000`

### 1.1 Codebase Audit

References to `5000` and registry forwarding in `ap` are centralized in three packages:
- `ap/pkg/imageref/naming.go` (`PushPrefix`): Maps the default `images.local` prefix to `localhost:5000` unless running in-cluster or using an in-cluster BuildKit host (`k8s://...`).
- `ap/pkg/images/build.go` (`BuildTasksForImages`): Wraps image build tasks in a `k8s.PortForwardTask` targeting `in-cluster-image-registry-system/svc/in-cluster-image-registry` (remote port 80 to `LocalPort: 5000`) if `push && naming.Prefix == "images.local" && !strings.HasPrefix(buildkitHost, "k8s://")`.
- `ap/pkg/k8s/portforward.go` (`PortForwardTask`): Executes `kubectl port-forward` to establish the tunnel. On macOS, when `LocalPort == 5000`, it allocates an ephemeral free port for `kubectl`, starts an `alpine/socat` container named `ap-registry-proxy` publishing `127.0.0.1:5000:5000` in the Docker VM, and forwards to `host.docker.internal:<freePort>`.

### 1.2 Path-by-Path Evaluation

#### Path A: Local Docker Build and Push (`docker buildx build --push`)
- **Trigger**: `ap build --push` or `ap deploy` targeting a non-kind cluster without `--buildkit-host`.
- **Programs talking to `localhost:5000`**:
  - **Linux**: The host Docker daemon (`dockerd`) connects via TCP to `127.0.0.1:5000`.
  - **macOS**: `dockerd` inside the Docker Desktop VM connects to `127.0.0.1:5000` inside the VM. The traffic is intercepted by the `ap-registry-proxy` container, forwarded across the hypervisor bridge via `host.docker.internal:<freePort>`, and routed through the host's `kubectl port-forward` process.
- **Does it talk to `localhost:5000` today?** **Yes.** This is the primary active user of `localhost:5000`.
- **Does it need a port-forward?** **Yes**, if `dockerd` directly performs the push. `dockerd` is outside the Kubernetes cluster network and cannot resolve internal cluster DNS names (such as `in-cluster-image-registry.in-cluster-image-registry-system.svc.cluster.local`) or route to internal ClusterIPs.

#### Path B: `buildctl` with Remote In-Cluster BuildKit (`k8s://...`)
- **Trigger**: `ap build --push --buildkit-host k8s` or `ap deploy ... --buildkit-host k8s` (or `BUILDKIT_HOST=k8s://<namespace>/<service>`).
- **Programs talking to `localhost:5000`**: **None.**
- **Does it talk to `localhost:5000` today?** **No.**
  - In `ap/pkg/images/build.go`: The code explicitly checks `!strings.HasPrefix(buildkitHost, "k8s://")`. No `PortForwardTask` is constructed.
  - In `ap/pkg/imageref/naming.go`: `PushPrefix()` rewrites `images.local` directly to the in-cluster service DNS:
    `in-cluster-image-registry.in-cluster-image-registry-system.svc.cluster.local:80`.
  - The local `buildctl` process communicates with BuildKit over a private Unix domain socket via `BuildkitForwarder`.
  - BuildKit runs *inside* the cluster and pushes directly to the in-cluster registry over the internal Kubernetes cluster network.
- **Does it need a port-forward?** **No.** It is already completely listener-free.

#### Path C: `buildctl` with Remote External TCP BuildKit (`tcp://...`)
- **Trigger**: `ap build --push --buildkit-host tcp://remote-host:2375`.
- **Programs talking to `localhost:5000`**: The remote `buildkitd` daemon attempts to dial `localhost:5000`.
- **Does it talk to `localhost:5000` today?** It attempts to, but is broken based on code inspection (**not empirically run**).
  - Code inspection of `ap/pkg/imageref/naming.go` and `ap/pkg/images/build.go`: For `tcp://` hosts, `PushPrefix` returns `localhost:5000`. `ap` opens a port-forward on the *developer's local machine*.
  - `buildctl` instructs the *remote* `buildkitd` daemon to push to `localhost:5000`. The remote daemon attempts to dial `localhost:5000` on the *remote* machine where it runs, where no registry or port-forward exists.
- **Does it need a port-forward?** A local host port-forward is useless for remote non-k8s BuildKit hosts.

#### Path D: Kind Load Path (`tc.IsKind() == true`)
- **Trigger**: `ap deploy` when the target cluster is a Kind cluster (the standard local dev workflow).
- **Programs talking to `localhost:5000`**: **None.**
- **Does it talk to `localhost:5000` today?** **No.**
  - In `ap/pkg/cmd/deploy.go`: Line 406 sets `push := !opt.SkipPush && !tc.IsKind()`. Because `push` is false, `BuildTasksForImages` skips constructing `PortForwardTask`.
  - `DockerBuildTask` builds locally with `--load` into the Docker daemon image store.
  - `KindLoadTask` (`ap/pkg/kindload/load.go`) executes `docker save -o images.tar` and imports the tar directly into the Kind container node using `docker exec -i <node> ctr --namespace=k8s.io images import -`.
- **Does it need a port-forward?** **No.** No network push or registry listener is used.

#### Path E: External Registry Override (`IMAGE_PREFIX != "images.local"`)
- **Trigger**: Projects configuring an external registry (e.g. `IMAGE_PREFIX=gcr.io/my-project`).
- **Programs talking to `localhost:5000`**: **None.**
- **Does it talk to `localhost:5000` today?** **No.** `BuildTasksForImages` only forwards when `naming.Prefix == "images.local"`. Docker pushes directly to the public/cloud registry endpoint over the Internet.
- **Does it need a port-forward?** **No.**

### 1.3 Path Summary Matrix

| Build Path | Uses Port-Forward Today? | Entity Dialing Target | Target Address | Needs Host Listener? | Status |
| :--- | :---: | :--- | :--- | :---: | :--- |
| **Local Docker (Linux)** | **Yes** | Host `dockerd` | `localhost:5000` | Yes (for direct push) | Works; opens TCP listener |
| **Local Docker (macOS)** | **Yes** | VM `dockerd` -> socat | `localhost:5000` | Yes (for direct push) | Works; opens 2 TCP listeners + proxy container |
| **Remote BuildKit (`k8s://`)** | **No** | In-cluster `buildkitd` | In-cluster service DNS (`:80`) | **No** | Already listener-free (Unix socket for RPC) |
| **Remote BuildKit (`tcp://`)** | **Yes** (attempts) | Remote `buildkitd` | `localhost:5000` (fails) | **No** (useless) | Broken by design (code analysis; not run) |
| **Kind Load (`tc.IsKind()`)** | **No** | Local `ctr images import` | Node container filesystem | **No** | Already listener-free (`docker save` + `ctr`) |
| **External Registry** | **No** | Host `dockerd` | Cloud/Remote Registry URL | **No** | Pushes to remote endpoint |

**Key Finding**: The **only** working path that actually talks to `localhost:5000` today is the **local Docker build and push path** against non-Kind clusters when `--buildkit-host` is omitted.

---

## 2. Alternatives to a TCP Listener for Local Host Programs

Four alternative communication models were evaluated: Unix domain socket reference handling and image archive export across different Docker storage drivers were empirically tested, while daemon proxy configuration is an architectural analysis and the in-process SPDY pusher workflow is an untried conceptual design.

### 2.1 Unix Domain Sockets (`unix:///path/to/socket`)

- **Hypothesis**: Replace the TCP listener with a private Unix domain socket (identical to the BuildKit forwarder pattern in `ap/pkg/k8s/buildkit_forward.go`).
- **Empirical Test**:
  Executed against Docker CLI and daemon:
  ```bash
  docker tag alpine:latest unix:///var/run/registry.sock/myimage:tag
  ```
- **Observed Result**:
  ```
  error parsing reference: "unix:///var/run/registry.sock/myimage:tag" is not a valid repository/tag: invalid reference format
  ```
- **Technical Analysis**:
  The OCI distribution specification and Docker reference grammar (in `github.com/distribution/reference` and `docker/reference`) strictly mandate the format `[domain[:port]/]path[:tag|@digest]`. A filesystem path or `unix://` scheme is rejected at reference parse time. Furthermore, the registry HTTP transport in Moby (`registry/transport.go`) initializes a standard `http.Transport` dialing TCP without support for Unix domain socket schemes (inferred from Moby source code; not verified by patching or reconfiguring dockerd). Docker cannot be configured to dial an image registry over a Unix domain socket.
- **Verdict**: **Infeasible** for Docker.

### 2.2 Daemon Proxy Settings (`HTTP_PROXY` / `HTTPS_PROXY`) — *Analysis Only*

- **Hypothesis**: Configure `dockerd` to route registry requests through an HTTP proxy without opening a local port.
- **Technical Analysis** (*not empirically tested*):
  1. Standard Go HTTP transports (used throughout Docker and Moby) automatically append loopback addresses (`127.0.0.1`, `localhost`) to the default `NO_PROXY` list. Bypassing this requires non-standard hostnames.
  2. Modifying `dockerd` proxy configuration requires root/administrative access to edit `/etc/docker/daemon.json` or systemd drop-in units and requires restarting the daemon. This disrupts developer environments and cannot be performed dynamically per CLI execution.
  3. The proxy itself would still need to accept connections from `dockerd`. If the proxy listens on a TCP port, the TCP listener is not eliminated; it is merely moved from `kubectl` to the proxy. If the proxy listened on a Unix socket, Docker's proxy client does not support Unix sockets for proxy destinations.
  4. On macOS, proxy configuration must be injected into the Docker Desktop VM settings, which cannot be automated transparently per command.
- **Verdict**: **Infeasible**; operational friction is prohibitive and fails to eliminate the listener.

### 2.3 `docker save` / OCI Archive Plus In-Process SPDY Push

- **Hypothesis**: Decouple the local build from the registry push. Have Docker build and export an image archive locally, and have `ap` stream the image layers and manifest directly to the registry using an in-process Kubernetes port-forward (SPDY connection).
- **Status of In-Process Pusher Workflow**: **Not tried (conceptual design and estimate)**.
  The workflow described below was analyzed conceptually based on existing Kubernetes SPDY primitives in `ap` (`ap/pkg/k8s/buildkit_forward.go`), but **was not executed or implemented on a live cluster in this investigation**.
- **Conceptual Workflow**:
  1. **Build & Export**: `ap` invokes Docker to build locally and export an archive (`docker save` or OCI layout).
  2. **In-Process Forwarder**: `ap` resolves the `in-cluster-image-registry` pod using its existing `client-go` connection and opens a SPDY port-forward data stream directly to container port `5000` (replicating `newBuildkitForwarderWithDialer` from `ap/pkg/k8s/buildkit_forward.go`).
  3. **In-Process Push**: An internal Go HTTP client in `ap` dials through the SPDY stream via a custom `http.Transport.DialContext`. `ap` sends standard OCI Distribution API v2 calls (`POST /v2/<name>/blobs/uploads/`, `PUT ...`, `PUT /v2/<name>/manifests/<tag>`) directly over the in-memory stream.

- **Empirical Tests on Image Export Across Docker Drivers and Image Stores**:
  Because the export step determines how images are extracted from Docker, two different Docker setups were tested:

  1. **Setup 1: Docker driver with containerd image store**:
     - *Configuration*: Docker Engine 29.9.3, `Storage Driver: overlayfs`, `driver-type: io.containerd.snapshotter.v1`, default buildx builder (driver: `docker`, BuildKit v0.34.2).
     - *Command*: `docker buildx build -t test-img:latest -o type=oci,dest=/tmp/testbuild/image.tar /tmp/testbuild`
     - *Result*: Succeeded. Produced a valid OCI layout archive containing:
       ```
       blobs/sha256/<hash> (layers and config)
       index.json
       oci-layout
       ```
  2. **Setup 2: Default `docker` driver with classic storage (no containerd image store)**:
     - *Configuration*: Docker Engine 29.10.0, `Storage Driver: overlay2` (classic graphdriver, no containerd snapshotter), default buildx builder (driver: `docker`, BuildKit v0.39.0).
     - *Command*: `docker buildx build -t test:latest -o type=oci,dest=/tmp/image.tar /tmp/build`
     - *Result*: **Refused**. Failed with explicit error:
       ```
       ERROR: failed to build: OCI exporter is not supported for the docker driver.
       Switch to a different driver, or turn on the containerd image store, and try again.
       Learn more at https://docs.docker.com/go/build-exporters/
       ```
  3. **Universal Alternative: `docker build --load` followed by `docker save`**:
     - *Configuration*: Tested on both Setup 1 and Setup 2.
     - *Command*: `docker buildx build -t test:latest --load /tmp/build && docker save -o /tmp/image.tar test:latest`
     - *Result*: **Succeeded on both setups**. Produced an image tarball containing `manifest.json`, `index.json`, `oci-layout`, and layer tar archives.

- **Implications for Archive Export**:
  An in-process push design cannot rely exclusively on `buildx -o type=oci` because users with the classic `docker` driver (overlay2 without containerd image store) will receive an exporter error. To support all Docker environments without requiring users to reconfigure their daemon or switch builders, the exporter must use `docker save` (or pipe `docker save` stdout directly into the pusher), matching the established pattern used by `KindLoadTask` in `ap/pkg/kindload/load.go`.

- **Advantages (if implemented)**:
  - **Zero TCP Listeners**: Absolutely no TCP port is opened on the host (`127.0.0.1` or otherwise).
  - **Eliminates macOS Socat Bridge**: Because `dockerd` never connects to a remote network endpoint, the macOS `socat` bridge container, `host.docker.internal` resolution, and dual-port allocation are **100% eliminated**.
  - **Security**: Port-forwarding is completely isolated within the authenticated, TLS-encrypted Kubernetes API server SPDY stream.

- **Costs & Trade-offs (Estimated)**:
  - Requires adding a lightweight OCI registry pusher in `ap` (estimated at ~200 lines in `ap/pkg/k8s/` or `ap/pkg/images/` using client-go SPDY streams, or importing `google/go-containerregistry`).
  - Layer caching: Direct `docker push` checks existing blobs on the registry (`HEAD /v2/<name>/blobs/<digest>`) and skips pushing cached layers. An in-process pusher in `ap` must perform this same `HEAD` check to avoid re-uploading unchanged layers over the Kubernetes API tunnel.

- **Verdict**: **Viable architectural alternative; workflow not yet implemented or run.**

### 2.4 `buildctl` with OCI Output Plus In-Process Push

- **Hypothesis**: For developers using a local BuildKit daemon, have `buildctl` export an OCI archive and push it in-process.
- **Empirical Test**:
  Inspected `buildctl build --help` (BuildKit v0.35.0):
  ```bash
  buildctl build --frontend dockerfile.v0 --local context=. --local dockerfile=. --output type=oci,dest=/tmp/image.tar
  ```
  BuildKit natively supports `--output type=oci` and `--output type=docker`. `ap` already implements `Load: true` archive exporting in `ap/pkg/images/build.go` (line 205).
- **Verdict**: **Viable**, though unnecessary for the standard in-cluster BuildKit path (`k8s://...`), which already pushes inside the cluster.

---

## 3. Hardening and Narrowing the Listener (If Retained)

If an in-process pusher is not immediately implemented and a TCP listener must temporarily be retained for local Docker builds, the listener should be narrowed to minimize exposure.

### 3.1 Random Free Ephemeral Port

- **Mechanism**: Instead of hardcoding `LocalPort: 5000`, `ap` dynamically allocates an ephemeral loopback port (e.g. via `net.Listen("tcp", "127.0.0.1:0")` or `k8s.PickFreePort()`). The image is tagged `127.0.0.1:<randomPort>/<name>:<tag>`.
- **Empirical Test**:
  1. Verified default Docker daemon insecure registry configuration (`docker info`):
     ```
     Insecure Registries:
      127.0.0.0/8
      ::1/128
     ```
     Docker treats the entire IPv4 loopback block (`127.0.0.0/8`) and IPv6 loopback (`::1/128`) on **any port** as insecure by default.
  2. Started `registry:2` listening on random ephemeral port `49153`.
  3. Executed `docker buildx build -t 127.0.0.1:49153/test:latest --push /tmp/testbuild` and `-t localhost:49153/test:latest --push`.
  - **Measurement**: Both builds and pushes succeeded without requiring daemon configuration or flags.
- **Manifest Reference Independence**:
  In-cluster image pulling is completely unaffected by the port used to push. The in-cluster node agent (`in-cluster-image-registry-node-agent`) configures containerd's `certs.d` to mirror `images.local` to the Service ClusterIP on port 80. The push port is ephemeral and client-side only.
- **Benefits**:
  - Eliminates collisions with macOS AirPlay (port 5000).
  - Eliminates collisions between concurrent `ap` builds.
  - Eliminates predictable, static target ports for local port scanners.

### 3.2 Binding Strictly to `127.0.0.1`

- **Current Behavior**: `PortForwardTask.buildArgs()` invokes:
  `kubectl port-forward -n in-cluster-image-registry-system svc/in-cluster-image-registry <port>:80`
  As documented in `kubectl port-forward --help`:
  ```
  --address=[localhost]:
      Addresses to listen on (comma separated). Only accepts IP addresses or localhost as a value.
      When localhost is supplied, kubectl will try to bind on both 127.0.0.1 and ::1 and will fail
      if neither of these addresses are available to bind.
  ```
  Without `--address`, `kubectl` defaults to `localhost` and binds both loopback addresses.
- **Narrowing**: Explicitly append `--address 127.0.0.1` to the `kubectl` arguments in `PortForwardTask.buildArgs()`. This restricts the socket strictly to the IPv4 loopback interface.

### 3.3 Temporal Scoping (Push-Only Window)

- **Current Behavior**: `PortForwardTask` wraps the entire `build-images` task group. The TCP port-forward (and macOS socat container) is established before compilation and stays open for the entire duration of all image builds.
- **Narrowing**: Separate the build phase from the push phase:
  1. Build all images locally to the local daemon/cache without pushing.
  2. Open the ephemeral port-forward.
  3. Push images in rapid succession.
  4. Immediately terminate the port-forward and clean up.
- **Exposure Reduction**: Reduces listener lifetime from minutes (multi-image build/compilation duration) to seconds (push duration).

### 3.4 Requirements for the macOS `socat` Bridge Under Narrowing

On macOS, Docker Desktop runs inside a Linux hypervisor. Pushing to `localhost:<port>` dials the VM's loopback, not the macOS host.
If a random port is used with the existing socat container bridge architecture:
1. **Dual-Port Allocation**:
   - `docker run -p 127.0.0.1:<vmPort>:<vmPort>` publishes `<vmPort>` in the VM and simultaneously binds `127.0.0.1:<vmPort>` on the macOS host via `com.docker.backend`.
   - Therefore, `kubectl port-forward` **cannot** bind `<vmPort>` on the host.
   - `ap` must pick **two** distinct free ports: `vmPort` (for Docker inside the VM) and `hostPort` (for `kubectl port-forward` on the host).
2. **Socat Bridge Invocation**:
   Socat must be launched with:
   ```bash
   docker run -d --name ap-registry-proxy-<uniqueID> \
     -p 127.0.0.1:<vmPort>:<vmPort> alpine/socat \
     TCP-LISTEN:<vmPort>,fork,reuseaddr TCP:host.docker.internal:<hostPort>
   ```
3. **Container Name Isolation**:
   The proxy container name must incorporate a unique execution ID or PID to prevent collisions during concurrent runs.

**Assessment**: While narrowing to a random ephemeral port solves port collisions and AirPlay interference, it does **not** eliminate the security risk of a local TCP listener and retains the fragile macOS dual-port socat bridge.

---

## 4. Recommendations and Options

### 4.1 The Smallest Change That Removes the Listener (In-Process Push for Local Builds)

The smallest surgical change that preserves `ap`'s current command-line behavior and user expectations—without imposing new cluster requirements—is implementing an in-process pusher for local Docker builds:

1. **Build locally with Docker**:
   Use `docker build ... --load` (or buildx with `--load`), followed by `docker save` (or piping `docker save` stdout). As demonstrated in Section 2.3, `docker save` works universally across classic `overlay2` and containerd image stores without requiring user-side daemon changes.
2. **Push in-process over SPDY**:
   Open an in-process port-forward stream directly to the `in-cluster-image-registry` pod on container port `5000` via client-go's SPDY dialer (replicating the pattern in `ap/pkg/k8s/buildkit_forward.go`). A minimal Go HTTP transport inside `ap` streams the layers and manifest over this connection.
3. **Delete the host listener and macOS bridge**:
   Remove `PortForwardTask` and the `alpine/socat` container bridge from `ap/pkg/k8s/portforward.go`.

**Cost / Trade-off**: Requires implementing and testing the in-process OCI upload logic in `ap` (estimated at ~200 lines, or utilizing an established client like `google/go-containerregistry`).

### 4.2 Alternative Option for Maintainer Consideration: Default to In-Cluster BuildKit (`--buildkit-host k8s`)

An alternative operational path is to make in-cluster BuildKit the default for remote cluster deployments:

- **Concept**: When deploying to a non-Kind cluster with `IMAGE_PREFIX=images.local`, `ap deploy` could default to `--buildkit-host k8s` (`k8s://autodeploy-system/buildkit`).
- **Advantage**: Requires almost zero new networking code in `ap`. The `k8s://` BuildKit path already exists, uses private Unix domain sockets on the host, and pushes directly to `in-cluster-image-registry...svc.cluster.local:80` inside the cluster (already 100% listener-free).
- **Costs & Trade-offs**:
  1. **Changes Default Behavior**: Alters how `ap deploy` operates by default for non-Kind clusters.
  2. **Requires In-Cluster BuildKit**: Mandates that the target Kubernetes cluster has a BuildKit deployment running (`autodeploy-system/buildkit`). If the cluster does not have BuildKit installed, the deployment fails unless the user falls back to local building.

### 4.3 What Would Be Left Under the Recommended Change

Once the in-process push change (Section 4.1) is implemented:
1. **Local TCP Listeners**: **0**. No process will listen on `localhost:5000`, `127.0.0.1`, or any ephemeral TCP port.
2. **Private Unix Domain Sockets**: Only the BuildKit forwarder socket (`/tmp/ap-bk-*/buildkit.sock`, permissions `0700`), active only during `buildctl` invocations.
3. **macOS Bridge Containers**: **0**. `alpine/socat` and `host.docker.internal` workarounds are completely deleted.
4. **AirPlay / Port Collision Workarounds**: Completely obsolete and removed.
5. **Applied Kubernetes Manifests**: Unchanged. Manifests continue to reference `images.local/<image>:<tag>@sha256:...`, which containerd pulls via the in-cluster node agent mirror.

---

## Appendix: Verification Log

| Topic / Test | Method / Setup | Result | Source / Reference |
| :--- | :--- | :--- | :--- |
| **Docker Unix Socket Ref** | `docker tag alpine:latest unix:///var/run/reg.sock/img:tag` | Rejected: `invalid reference format` | Docker CLI & reference grammar |
| **Docker Random Port Push** | `docker buildx build -t 127.0.0.1:49153/test:latest --push` | Succeeded; image pushed to registry | Tested with local `registry:2` on port 49153 |
| **Docker Loopback Insecure**| `docker info` | `127.0.0.0/8` and `::1/128` listed in `Insecure Registries` | Docker Community Engine v29.9.3 / v29.10.0 |
| **Buildx OCI Export (containerd store)** | `docker buildx build -o type=oci,dest=/tmp/test.tar` | Succeeded; created valid OCI layout | Docker 29.9.3 with containerd snapshotter |
| **Buildx OCI Export (classic overlay2)** | `docker buildx build -o type=oci,dest=/tmp/test.tar` | Refused: `OCI exporter is not supported for the docker driver` | Docker 29.10.0 with classic overlay2 storage |
| **Buildx `--load` + `docker save`** | `docker buildx build --load` then `docker save` | Succeeded on both storage configurations | Universal export format across all Docker setups |
| **In-Process Pusher Workflow** | Conceptual design (SPDY stream to registry pod) | **Not tried** on live cluster | Estimated at ~200 lines based on `buildkit_forward.go` |
| **Buildctl OCI Support** | `buildctl build --help` | Flags `-o type=oci` and `type=docker` verified | BuildKit v0.35.0 CLI inspection |
| **Kubectl Default Bind** | `kubectl port-forward --help` | Binds both `127.0.0.1` and `::1` when `--address` is omitted | kubectl v1.37.2 CLI documentation |
| **AirPlay Port 5000** | macOS Monterey 12+ default configuration | `ControlCenter` listens on `*:5000` | macOS system documentation & `portforward.go:134` |
| **Path C (`tcp://` BuildKit)** | Codebase audit of `build.go` and `naming.go` | Incompatible; passes local address to remote daemon | Code inspection (**not run**) |
| **Kind Load Listener Check**| Codebase audit of `deploy.go` & `kindload/load.go` | Sets `push=false`, uses `docker save` & `ctr import` | Codebase audit |
| **In-Cluster BuildKit Check**| Codebase audit of `build.go` & `naming.go` | Skips `PortForwardTask`, uses Service DNS directly | Codebase audit |
