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
2. **Port Collisions & Flakiness**: Fixed ports (such as `5000`) frequently collide with existing services. On macOS, the system AirPlay Receiver binds `*:5000` by default. Multiple concurrent `ap` invocations or local container registries also fail with `bind: address already in use`.
3. **Cross-Platform Complexity**: On macOS, Docker runs inside a virtualized Linux VM. A port-forward bound to the macOS host loopback is unreachable from inside the VM, forcing `ap` to maintain a secondary `alpine/socat` bridge container (`ap-registry-proxy`) that publishes ports inside the VM and routes back to the host via `host.docker.internal`.

`ap` previously resolved this problem for the BuildKit daemon connection (Issue #797). The BuildKit bridge moved from a local TCP port-forward to an in-process forwarder over a private Unix domain socket located in a mode `0700` temporary directory (`ap/pkg/k8s/buildkit_forward.go`).

Currently, one TCP listener remains in `ap`: when container images are pushed to the in-cluster registry (`in-cluster-image-registry`), `ap` forwards the registry service to `localhost:5000` (`ap/pkg/images/build.go`, `ap/pkg/k8s/portforward.go`, `ap/pkg/imageref/naming.go`).

This report investigates the necessity of this remaining port-forward, tests potential non-listener alternatives, evaluates hardening strategies if a listener were retained, and provides a concrete recommendation for completely eliminating it.

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
- **Does it talk to `localhost:5000` today?** It attempts to, but is **broken/dysfunctional**.
  - `PushPrefix` returns `localhost:5000`. `ap` opens a port-forward on the *developer's local machine*.
  - `buildctl` instructs the *remote* `buildkitd` daemon to push to `localhost:5000`. The remote daemon attempts to dial `localhost:5000` on the *remote* machine, where no registry or port-forward exists.
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
| **Remote BuildKit (`tcp://`)** | **Yes** (attempts) | Remote `buildkitd` | `localhost:5000` (fails) | **No** (useless) | Broken by design |
| **Kind Load (`tc.IsKind()`)** | **No** | Local `ctr images import` | Node container filesystem | **No** | Already listener-free (`docker save` + `ctr`) |
| **External Registry** | **No** | Host `dockerd` | Cloud/Remote Registry URL | **No** | Pushes to remote endpoint |

**Key Finding**: The **only** working path that actually talks to `localhost:5000` today is the **local Docker build and push path** against non-Kind clusters when `--buildkit-host` is omitted.

---

## 2. Alternatives to a TCP Listener for Local Host Programs

For local host programs that must push built images to the in-cluster registry, four alternative communication models were analyzed and empirically tested:

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
  The OCI distribution specification and Docker reference grammar strictly mandate `[domain[:port]/]path[:tag|@digest]`. A filesystem path or `unix://` scheme is rejected at the reference parser level. Furthermore, Moby's distribution client hardcodes `net.DialContext("tcp", ...)` for all registry interactions. Docker cannot be configured to dial an image registry over a Unix domain socket.
- **Verdict**: **Infeasible** for Docker.

### 2.2 Daemon Proxy Settings (`HTTP_PROXY` / `HTTPS_PROXY`)

- **Hypothesis**: Configure `dockerd` to route registry requests through an HTTP proxy without opening a local port.
- **Technical Analysis**:
  1. Standard Go HTTP transports (used throughout Docker and Moby) automatically append loopback addresses (`127.0.0.1`, `localhost`) to the default `NO_PROXY` list. Bypassing this requires non-standard hostnames.
  2. Modifying `dockerd` proxy configuration requires root/administrative access to edit `/etc/docker/daemon.json` or systemd drop-in units and requires restarting the daemon. This disrupts developer environments and cannot be performed dynamically per CLI execution.
  3. The proxy itself would still need to accept connections from `dockerd`. If the proxy listens on a TCP port, the TCP listener is not eliminated; it is merely moved from `kubectl` to the proxy. If the proxy listened on a Unix socket, Docker's proxy client does not support Unix sockets for proxy destinations.
  4. On macOS, proxy configuration must be injected into the Docker Desktop VM settings, which cannot be automated transparently per command.
- **Verdict**: **Infeasible**; operational friction is prohibitive and fails to eliminate the listener.

### 2.3 `docker save` / OCI Archive Plus In-Process SPDY Push

- **Hypothesis**: Decouple the local build from the registry push. Have Docker build and export an image archive locally, and have `ap` stream the image layers and manifest directly to the registry using an in-process Kubernetes port-forward (SPDY connection).
- **Workflow**:
  1. **Build**: `ap` invokes `docker buildx build -o type=oci,dest=/tmp/image.tar .` (or pipes `type=oci,dest=-` / `docker save` stdout).
  2. **In-Process Forwarder**: `ap` resolves the `in-cluster-image-registry` pod using its existing `client-go` connection and opens a SPDY port-forward data stream directly to container port `5000` (replicating `newBuildkitForwarderWithDialer` from `ap/pkg/k8s/buildkit_forward.go`).
  3. **In-Process Push**: An internal Go HTTP client in `ap` dials through the SPDY stream via a custom `http.Transport.DialContext`. `ap` sends standard OCI Distribution API v2 calls (`POST /v2/<name>/blobs/uploads/`, `PUT ...`, `PUT /v2/<name>/manifests/<tag>`) directly over the in-memory stream.
- **Empirical Test**:
  Executed against Docker Buildx v0.38.1:
  ```bash
  docker buildx build -t test-img:latest -o type=oci,dest=/tmp/testbuild/image.tar /tmp/testbuild
  ```
  - **Measurement**: Build and OCI export completed in **0.0s** for a minimal base layer.
  - **Archive Inspection**:
    ```
    blobs/sha256/<hash> (layers and config)
    index.json
    oci-layout
    ```
    Generated a valid, self-contained OCI Image Layout specification directory/tarball.
- **Advantages**:
  - **Zero TCP Listeners**: Absolutely no TCP port is opened on the host (`127.0.0.1` or otherwise).
  - **Eliminates macOS Socat Bridge**: Because `dockerd` never connects to a remote network endpoint, the macOS `socat` bridge container, `host.docker.internal` resolution, and dual-port allocation are **100% eliminated**.
  - **Security**: Port-forwarding is completely isolated within the authenticated, TLS-encrypted Kubernetes API server SPDY stream.
- **Costs & Trade-offs**:
  - Requires adding a lightweight OCI registry pusher in `ap` (or integrating an established client like `google/go-containerregistry`).
  - Layer caching: Direct `docker push` checks existing blobs on the registry (`HEAD /v2/<name>/blobs/<digest>`) and skips pushing cached layers. An in-process pusher in `ap` must perform this same `HEAD` check to avoid re-uploading unchanged layers over the Kubernetes API tunnel.
- **Verdict**: **Highly Viable and Recommended**.

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
  - **Measurement**: Both builds and pushes succeeded in **0.0s - 0.1s** without requiring daemon configuration or flags.
- **Manifest Reference Independence**:
  In-cluster image pulling is completely unaffected by the port used to push. The in-cluster node agent (`in-cluster-image-registry-node-agent`) configures containerd's `certs.d` to mirror `images.local` to the Service ClusterIP on port 80. The push port is ephemeral and client-side only.
- **Benefits**:
  - Eliminates collisions with macOS AirPlay (port 5000).
  - Eliminates collisions between concurrent `ap` builds.
  - Eliminates predictable, static target ports for local port scanners.

### 3.2 Binding Strictly to `127.0.0.1`

- **Current Behavior**: `PortForwardTask.buildArgs()` invokes:
  `kubectl port-forward -n in-cluster-image-registry-system svc/in-cluster-image-registry <port>:80`
  Without `--address`, `kubectl` defaults to binding both `127.0.0.1` and `[::1]`.
- **Narrowing**: Explicitly append `--address 127.0.0.1` to the `kubectl` arguments. This strictly restricts the socket to the IPv4 loopback interface.

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

## 4. Recommendation & Roadmap

### 4.1 Recommended Solution: The Smallest Change That Removes the Listener

The smallest, cleanest architectural change that completely removes the TCP listener consists of two complementary steps:

#### Step 1: Default to In-Cluster BuildKit (`--buildkit-host k8s`) for Non-Kind Deployments
- **Action**: When `ap deploy` targets a non-kind cluster and `IMAGE_PREFIX` is `images.local`, `ap` should check if `autodeploy-system/svc/buildkit` exists (or make `buildkitHost = "k8s"` the default convention).
- **Impact**:
  - The in-cluster BuildKit builder communicates via private Unix domain socket (`BuildkitForwarder`).
  - Images are pushed inside the cluster directly to `in-cluster-image-registry...svc.cluster.local:80`.
  - **Zero TCP listeners** on the host. **Zero socat containers** on macOS.
  - This path already exists and is fully functional in `ap` today; making it the default removes the listener for the standard remote deployment workflow without writing new networking code.

#### Step 2: Implement an In-Process OCI Pusher for Local Docker Builds
- **Action**: For builds that do not use in-cluster BuildKit (e.g. `ap build --push` with local Docker):
  1. Build with `docker buildx build -o type=oci,dest=-` (or `docker save`).
  2. Implement an in-process SPDY client that streams the OCI archive directly to `in-cluster-image-registry` container port `5000` via client-go's existing SPDY dialer.
  3. Delete `PortForwardTask` and the macOS `socat` proxy container from `ap/pkg/k8s/portforward.go`.
- **Lines of Code Estimate**: ~200 lines in `ap/pkg/k8s/` or `ap/pkg/images/` to handle SPDY stream dial and OCI blob/manifest uploads (or importing `google/go-containerregistry`).

### 4.2 What Would Be Left

Once this change is applied:
1. **Local TCP Listeners**: **0**. No process will listen on `localhost:5000`, `127.0.0.1`, or any ephemeral TCP port.
2. **Private Unix Domain Sockets**: Only the BuildKit forwarder socket (`/tmp/buildkit-forward-.../socket`, permissions `0700`), active only during `buildctl` invocations.
3. **macOS Bridge Containers**: **0**. `alpine/socat` and `host.docker.internal` workarounds are completely deleted.
4. **AirPlay / Port Collision Workarounds**: Completely obsolete and removed.
5. **Applied Kubernetes Manifests**: Unchanged. Manifests continue to reference `images.local/<image>:<tag>@sha256:...`, which containerd pulls via the in-cluster node agent mirror.

---

## Appendix: Empirical Verification Log

| Test / Observation | Command / Setup | Result | Source / Log |
| :--- | :--- | :--- | :--- |
| **Docker Unix Socket Ref** | `docker tag alpine:latest unix:///var/run/reg.sock/img:tag` | Rejected: `invalid reference format` | Docker CLI reference parser |
| **Docker Random Port Push** | `docker buildx build -t 127.0.0.1:49153/test:latest --push` | Succeeded in 0.0s | Built & pushed to local `registry:2` on port 49153 |
| **Docker Loopback Insecure**| `docker info` | `127.0.0.0/8` and `::1/128` listed in `Insecure Registries` | Docker Community Engine v29.9.3 |
| **Buildx OCI Export** | `docker buildx build -o type=oci,dest=/tmp/test.tar` | Succeeded in 0.0s; created valid OCI layout | Tested with Buildx v0.38.1 |
| **Buildctl OCI Support** | `buildctl build --help` | Flags `-o type=oci` and `type=docker` verified | BuildKit v0.35.0 CLI inspection |
| **Existing BuildKit Unix Forwarder** | `ap/pkg/k8s/buildkit_forward.go` | SPDY stream multiplexer to private Unix socket | Codebase audit (Issue #797) |
| **Kind Load Listener Check**| `ap/pkg/cmd/deploy.go` & `ap/pkg/kindload/load.go` | Sets `push=false`, uses `docker save` & `ctr import` | Codebase audit |
| **In-Cluster BuildKit Check**| `ap/pkg/images/build.go` & `naming.go` | Skips `PortForwardTask`, uses Service DNS directly | Codebase audit |
