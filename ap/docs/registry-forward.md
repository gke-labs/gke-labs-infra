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

This report investigates the build paths that use this listener, tests alternative zero-listener communication models, evaluates containerized BuildKit with OCI directory export and in-process pushing, and documents the landed design.

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

## 2. Chosen Approach: Containerized BuildKit with OCI Directory Export and Service Proxy Push

### 2.1 Architecture Overview

To eliminate the `localhost:5000` listener entirely while avoiding per-build archive export bottlenecks, the chosen architecture structures local builds as follows:

1. **Build Daemon**: `ap` builds using a BuildKit daemon running inside the local `ap-builder` Docker container (`buildx_buildkit_ap-builder0`, ensured by `ap/pkg/images/build.go`'s `ensureMultiPlatformBuilder`). The host `buildctl` binary communicates with the container using `buildctl --addr docker-container://buildx_buildkit_ap-builder0`, multiplexing gRPC commands directly over container stdio (`docker exec`). **No TCP port is opened on the host or inside the container.**
2. **OCI Layout Directory Export**: BuildKit exports the build result directly to an **OCI layout directory on the host** using `--output type=oci,tar=false,dest=<dir>`. It does not create tar archives or invoke `docker save`.
3. **In-Process Push via API Server Service Proxy (`ap/pkg/registrypush`)**:
   `ap` pushes the image from the OCI layout directory to the in-cluster registry **in-process** via the landed `ap/pkg/registrypush` package (`registrypush.NewTransport`, `registrypush.Push`).
   - The pusher routes HTTP requests directly through the Kubernetes API server's service proxy subresource:
     `/api/v1/namespaces/in-cluster-image-registry-system/services/in-cluster-image-registry:5000/proxy`
   - The transport is built directly from the cluster kubeconfig using client-go's `rest.TransportFor(clientConfig)`.
   - **Why this was chosen over SPDY port-forward streams**: Plain HTTP through the service proxy requires no SPDY stream framing, no pod discovery (`FindReadyPod`), and no hand-written connection wrappers (`net.Conn` over SPDY streams). It uses standard HTTP `RoundTripper` delegation (~90 lines of code in `transport.go`).
4. **Zero Host Listeners, Zero macOS Proxies**: No local port is bound on loopback, and no `alpine/socat` proxy container or `host.docker.internal` bridge is required on macOS.

### 2.2 Known Limits of the Service Proxy Route

The API server service proxy route has the following operational characteristics and known limits:
1. **RBAC Permissions**: The user's Kubernetes credentials must have permission to proxy to the service (`services/proxy` on `in-cluster-image-registry` in `in-cluster-image-registry-system`), whereas port-forwarding requires `pods/portforward`.
2. **Per-Request Endpoint Selection**: The Kubernetes API server service proxy picks an endpoint independently per HTTP request. Because `in-cluster-image-registry` is deployed as a single-replica StatefulSet (`replicas: 1`), all requests consistently reach the single backing registry instance.
3. **Performance on Kind**: Measured on a local Kind cluster with a standalone probe using the same route (client-go transport + a rewriting round tripper + go-containerregistry): pushing two 400 MiB layers took **2.4 s**; an unchanged repeated push was observed to take two HTTP requests and skips re-uploading layers. (The `ap/pkg/registrypush` package itself was verified via the end-to-end test `TestRegistryPush`).
4. **Untested on Cloud Providers**: The service proxy push has been tested on Kind; it **has not been tried against GKE**.

---

### 2.3 Facts Established During Review (Attributed as "Checked During Review")

The following properties were verified and documented during review:

- **Export Cost of Archive Routes**:
  Running `docker save` on an image that Docker lists at 973 MB (302 MB compressed archive) required **2.0–2.4 s** per run with no changes to the image. This overhead is incurred unconditionally on every single build, making tar archive export unsuited as the primary local build path.
- **Directory Exporter Skips Unchanged Layers**:
  Tested using a throwaway `moby/buildkit:buildx-stable-1` container (BuildKit v0.34.2) and a test image with a 200 MB layer. Exporting via `--output type=oci,tar=false` across three runs showed:
  - First export: 2.6 s.
  - Rebuild with no changes: reported "exporting layers done" in 0.0 s; the 200 MB layer blob's inode and modification timestamp were completely unchanged.
  - Rebuild with a small changed top layer: reported 0.0 s; the 200 MB blob remained untouched, and only the new small layer blobs were written.
  *(Limits of that initial review check: `buildctl` was executed from inside the BuildKit container; evaluated against one BuildKit version).*
- **Old Blobs Accumulate**:
  In the OCI layout output directory, old layer and manifest blobs accumulate across builds (e.g. 11 files present after three builds). Consequently:
  - The push step must strictly parse the active `index.json` and manifest to push only currently referenced blobs (implemented in `ap/pkg/registrypush`).
  - The output directory requires periodic pruning or clean-up.
- **Privilege Requirements**:
  A BuildKit container requires `--privileged` (or the rootless image with seccomp and AppArmor set to unconfined, as documented in BuildKit's documentation; the rootless configuration was not empirically run). This requirement is not new: the `docker-container` driver in Docker Buildx operates identically, and any workstation that has executed `ap`'s multi-platform build path already runs such a container (`buildx_buildkit_ap-builder0` with `privileged=true`).

---

### 2.4 Host `buildctl` Export Verification over `docker-container://`

To verify that the directory export behavior holds when `buildctl` runs on the host and communicates over Docker stdio, an empirical test was conducted using BuildKit v0.35.0 (`/tmp/buildctl`) on the host connected to container `test-bk` (`moby/buildkit:buildx-stable-1`):

- **Setup**: A Dockerfile containing a base `alpine:latest` stage and a 200 MB random layer (`COPY bigfile /bigfile`, 209,715,200 bytes).
- **Execution Command**:
  ```bash
  PATH=".../docker/bin:$PATH" buildctl --addr docker-container://test-bk build \
    --frontend dockerfile.v0 \
    --local context=/tmp/btest/context --local dockerfile=/tmp/btest/context \
    --output type=oci,tar=false,dest=/tmp/btest/out
  ```
- **Observations & Measured Timings**:
  1. **First Build**:
     - Completed in **6.5 s** (transferred 209.75 MB context, resolved Alpine base, created layer blob).
     - Produced 200 MB layer blob `04bfbab4264b8168eae1dc1236f5f4231d82368483549b1ea6f1510efe6b36b7` (inode `53620841`, size `209,779,374` bytes), along with base layer, config, and manifest blobs (4 files total in `blobs/sha256/`).
  2. **Unchanged Rebuild**:
     - Completed in **0.21 s** (`real 0m0.211s`, user `0.043s`, sys `0.037s`).
     - BuildKit reported `#6 COPY bigfile /bigfile CACHED` and `#7 exporting layers done` in 0.0 s.
     - Inode inspection confirmed blob `04bfbab...` retained inode `53620841` and its original modification timestamp. Unchanged blobs were skipped completely.
  3. **Small Change Rebuild**:
     - Added a small file `smallfile` (12 bytes) and `COPY smallfile /smallfile` to the Dockerfile.
     - Completed in **0.27 s** (`real 0m0.272s`, user `0.045s`, sys `0.047s`).
     - BuildKit cached the 200 MB layer (`#6 COPY bigfile /bigfile CACHED`) and exported layers in 0.0 s.
     - Inode inspection confirmed the 200 MB blob `04bfbab...` retained inode `53620841`. Only the new 118-byte layer (`37566b1...`), new config, and new manifest were written.
     - Total files in `blobs/sha256/` grew from 4 to 7 files, confirming that previous blobs are retained on disk and require selective pushing via `index.json`.

---

### 2.5 Builder Container Management: Reusing `ap-builder`

- **Mechanism**: `ap` already ensures a multi-platform builder in `ap/pkg/images/build.go` (`ensureMultiPlatformBuilder`) using `docker buildx create --name ap-builder --driver docker-container --driver-opt network=host --use`. When booted (e.g. via `buildx inspect --bootstrap ap-builder`), Buildx creates container `buildx_buildkit_ap-builder0`.
- **Empirical Check**: Ran `buildctl --addr docker-container://buildx_buildkit_ap-builder0 du` from the host. BuildKit responded and dialed successfully. Inspecting the container confirmed image `moby/buildkit:buildx-stable-1`, `Privileged: true`, and persistent state stored in named Docker volume `buildx_buildkit_ap-builder0_state` mounted at `/var/lib/buildkit`.
- **Lifecycle & Failure Behavior**: Buildx sets restart policy `unless-stopped`. If the container cannot start (e.g. host security policy forbids `--privileged` containers or Docker daemon is stopped), `ap` will **fail fast with a clear diagnostic error** explaining how to start or permit the container.

---

### 2.6 Impact on Current Working Build Paths

Adopting containerized BuildKit for the local build path has the following effects across `ap` workflows:

1. **Local Docker Path (Non-Kind Cluster, No BuildKit Host)**:
   - **Builder Change**: Switches from `docker buildx build` (host daemon) to `buildctl` targeting the containerized BuildKit daemon over stdio.
   - **Cache Isolation**: Build cache resides in the BuildKit container's volume (`/var/lib/buildkit`) rather than Docker daemon's storage.
   - **Image Store**: Built images are exported to the OCI directory on the host and pushed in-process to the cluster registry. They are not stored in the host Docker daemon's local image list (`docker images`) unless explicitly requested (e.g. `--load`).
   - **Listener Impact**: Completely eliminates `localhost:5000`, `PortForwardTask`, and the macOS `socat` proxy container.
2. **Kind Load Path (`tc.IsKind() == true`)**:
   - **Remains Unchanged**: Continues building locally with `--load` and importing via `docker save` + `ctr images import`. It opens no listeners today and has no reason to change.
3. **Remote In-Cluster BuildKit Path (`--buildkit-host k8s`)**:
   - **Remains Unchanged**: Continues communicating with the in-cluster daemon over a private Unix domain socket and pushing directly within the cluster network. It is already 100% listener-free.
4. **External Registry Path (`IMAGE_PREFIX != "images.local"`)**:
   - Continues pushing directly to external registry endpoints over standard HTTPS.

---

## 3. Evaluated Alternatives

The following alternatives were analyzed and are ranked in order of viability:

### 3.1 Alternative A: In-Process SPDY Port-Forward Stream Push

- **Concept**:
  - Open a direct SPDY port-forward connection to container port 5000 on the `in-cluster-image-registry` pod (using client-go's SPDY transport), wrap the data stream in a `net.Conn`, and stream HTTP registry requests over that in-memory connection.
  - This route was prototyped during the investigation and verified end-to-end (code in Appendix B).
- **Why It Was Dropped**:
  - Dropped in favor of the API server service proxy route (`ap/pkg/registrypush`) due to implementation size and complexity: the SPDY stream route requires ~300 lines of custom SPDY stream negotiation, pod lifecycle discovery (`FindReadyPod`), and a hand-written `net.Conn` wrapper, whereas the service proxy route uses standard HTTP `RoundTripper` delegation in under 90 lines.

### 3.2 Alternative B: BuildKit in Docker Pushing to a Stdio-Tunneled Proxy Container (*Not Tried*)

- **Concept**:
  - Run BuildKit in a Docker container on a private, isolated Docker network (`bridge`).
  - Run a lightweight proxy container on that same Docker network (e.g. `ap-registry-tunnel`) listening on `registry.local:5000`.
  - The proxy container attaches to `ap` over container stdio (`docker exec` or `docker attach`), tunneling HTTP traffic to `ap`.
  - `ap` forwards that tunneled traffic over the API server to the in-cluster registry.
  - BuildKit builds with `--output type=image,name=registry.local:5000/image:tag,push=true`.
- **Advantages**:
  - Eliminates the OCI export step entirely; layers are streamed during the build.
  - Leaves zero files on the host disk (no OCI directory to prune).
- **Disadvantages & Why It Is Not First**:
  - **Not tried**: Requires authoring and maintaining a custom proxy container image that bridges TCP on the private container network to stdio.
  - Adds multi-container lifecycle complexity and custom protocol multiplexing.

### 3.3 Alternative C: `--load` + `docker save` + In-Process Push

- **Concept**:
  - Build locally with standard Docker (`docker build ... --load`), export via `docker save`, and stream the tarball layers in-process over the API server proxy to the in-cluster registry.
- **Empirical Findings**:
  - **Universally Supported**: Tested across both containerd snapshotter storage (Docker 29.9.3) and classic graphdriver storage (`overlay2` without containerd store in Docker 29.10.0). Both successfully export `docker save` archives.
  - (In contrast, direct `docker buildx build -o type=oci,dest=...` was empirically **refused** on classic overlay2 storage with error: `OCI exporter is not supported for the docker driver. Switch to a different driver, or turn on the containerd image store`).
- **Disadvantage & Why It Is Not First**:
  - Incurs the **2.0–2.4 s** archive export penalty on every build, even when nothing has changed.

### 3.4 Alternative D: Narrowing the Existing TCP Listener

If a TCP listener were temporarily retained, it could be hardened:
1. **Random Ephemeral Loopback Port**:
   - Dynamically allocate a free port (`net.Listen("tcp", "127.0.0.1:0")` or `PickFreePort()`).
   - Empirically verified: Docker's default configuration lists `127.0.0.0/8` and `::1/128` as insecure registries (`docker info`). Tested pushing to random port `49153`; Docker pushed successfully without configuration changes.
   - Solves port collisions with macOS AirPlay (`*:5000`) and concurrent builds.
2. **Explicit Loopback Binding**:
   - Append `--address 127.0.0.1` to `kubectl port-forward` (documented in `kubectl port-forward --help` to avoid binding `::1` or non-loopback addresses).
3. **Temporal Scoping**:
   - Keep the forwarder open only during the push phase rather than across the entire build.
- **Why It Is Not Recommended**:
  - Does not eliminate the host TCP listener.
  - On macOS, Docker VM loopback isolation still requires dual-port allocation and the `alpine/socat` proxy container bridge.

### 3.5 Non-Viable Alternatives

- **Unix Domain Socket References**:
  Tested `docker tag alpine:latest unix:///var/run/registry.sock/test:tag`. Rejected by Docker reference grammar (`invalid reference format`). Moby's distribution client initializes standard TCP `http.Transport` instances without socket support (inferred from Moby source code; not verified by patching).
- **Daemon HTTP Proxy Configuration**:
  Requires modifying host `/etc/docker/daemon.json` and restarting the daemon. Cannot be managed transparently per CLI invocation, and loopback destinations bypass standard proxy transports by default.

---

## 4. Recommendations and Implementation Roadmap

### 4.1 Implementation Status & Next Steps

1. **Step 1: In-Process Service Proxy Pusher (Landed)**:
   - Added `ap/pkg/registrypush` implementing `Push(ctx, transport, directory, repository, tag)` and `NewTransport(...)` using the API server service proxy.
   - Verified in unit tests and Kind E2E tests (`TestRegistryPush`).
2. **Step 2: Wire `ap build` and `ap deploy`**:
   - Configure local Docker builds to use `buildctl --addr docker-container://buildx_buildkit_ap-builder0`, export to a temporary OCI layout directory (`--output type=oci,tar=false,dest=<dir>`), and push using `registrypush.Push`.
   - If the BuildKit container cannot start, fail fast with a clear diagnostic error explaining how to start or permit the container.
3. **Step 3: Delete the Port-Forward and Proxy Code**:
   - Delete `PortForwardTask` and the macOS `socat` bridge container from `ap/pkg/k8s/portforward.go`.
4. **Optional Maintainer Setting: Default to In-Cluster BuildKit (`--buildkit-host k8s`)**:
   - For non-Kind remote clusters that have `autodeploy-system/buildkit` deployed, `ap` could default to `--buildkit-host k8s`. This requires zero local building and zero host listeners, but alters default CLI behavior and requires in-cluster BuildKit deployment.

### 4.2 End State After Implementation

Following this change:
1. **Host TCP Listeners**: **0**. No process will bind `localhost:5000` or any ephemeral TCP port.
2. **Private Unix Domain Sockets**: Only active during in-cluster BuildKit forwarding (`/tmp/ap-bk-*/buildkit.sock`, mode `0700`).
3. **macOS Bridge Containers**: **0**. The `alpine/socat` container bridge and `host.docker.internal` routing are completely eliminated.
4. **AirPlay Collisions**: Completely eliminated.
5. **Applied Kubernetes Manifests**: Remain unchanged. Manifests reference `images.local/<image>:<tag>@sha256:...`, pulled via the node agent's containerd mirror.

---

## Appendix A: Verification Log

| Topic / Test | Method / Setup | Result | Source / Reference |
| :--- | :--- | :--- | :--- |
| **Archive Export Cost** | `docker save` on 973 MB image (302 MB archive) | Took 2.0–2.4 s per run with no changes | Checked during review |
| **Directory Exporter Unchanged Blobs** | BuildKit v0.34.2 inside container, 200 MB layer | 2.6 s initial; 0.0 s rebuild with same inode/mtime | Checked during review |
| **Old Blobs Accumulation** | Three successive OCI directory exports | 11 files accumulated; requires pruning & index parsing | Checked during review |
| **Privilege Requirement** | BuildKit container requirements | Needs `--privileged` (or rootless with unconfined) | Checked during review & BuildKit docs |
| **Host buildctl over `docker-container://`** | Host `buildctl` v0.35.0 dialing `docker-container://test-bk` | Initial: 6.5 s; unchanged: 0.21 s (skipped); small change: 0.27 s | Measured on test setup with 200 MB layer |
| **Buildx ap-builder Container Reuse** | Inspected `buildx_buildkit_ap-builder0` settings | Responds to `buildctl du`; state in named Docker volume | Measured on test setup; `build.go` (`ensureMultiPlatformBuilder`) |
| **Service Proxy Push on Kind** | Standalone probe on Kind using same route (client-go transport + roundtripper + go-containerregistry) | Two 400 MiB layers in 2.4 s; repeated push is 2 requests | Measured with standalone probe; TestRegistryPush verified package |
| **Service Proxy Push on GKE** | Pushing via `/services/...:5000/proxy` against GKE cluster | Not tried against GKE | Landed in `ap/pkg/registrypush` |
| **In-Process SPDY Push Prototype** | Go script dialing registry pod port 5000 via SPDY | Pushed blobs/manifest; pulled & ran in Kind pod | Prototyped during investigation (Appendix B) |
| **Classic Docker Driver OCI Refusal** | `docker buildx build -o type=oci` on overlay2 (no containerd store) | Refused: `OCI exporter is not supported for the docker driver` | Measured on Docker 29.10.0 DinD container |
| **`docker save` Universal Export** | `docker build --load` followed by `docker save` | Succeeded on both classic and containerd store setups | Measured across Docker 29.9.3 and 29.10.0 |
| **Docker Unix Socket Ref** | `docker tag alpine:latest unix:///var/run/reg.sock/img:tag` | Rejected: `invalid reference format` | Docker CLI & reference grammar |
| **Docker Random Port Push** | `docker buildx build -t 127.0.0.1:49153/test:latest --push` | Succeeded; image pushed to registry on port 49153 | Measured on test setup with local registry:2 |
| **Docker Loopback Insecure** | `docker info` inspection | `127.0.0.0/8` and `::1/128` listed in `Insecure Registries` | Docker Community Engine v29.9.3 / v29.10.0 |
| **Kubectl Default Bind** | `kubectl port-forward --help` | Binds both `127.0.0.1` and `::1` when `--address` is omitted | kubectl v1.37.2 CLI documentation |
| **AirPlay Port 5000** | macOS Monterey 12+ default configuration | `ControlCenter` listens on `*:5000` | macOS system documentation & `portforward.go:134` |
| **Path C (`tcp://` BuildKit)** | Codebase audit of `build.go` and `naming.go` | Incompatible; passes local address to remote daemon | Code inspection (**not run**) |
| **Kind Load Listener Check** | Codebase audit of `deploy.go` & `kindload/load.go` | Sets `push=false`, uses `docker save` & `ctr import` | Codebase audit |
| **In-Cluster BuildKit Check** | Codebase audit of `build.go` & `naming.go` | Skips `PortForwardTask`, uses Service DNS directly | Codebase audit |
| **Tunnel Proxy Container** | BuildKit in docker pushing to proxy container over stdio | Conceptual alternative | **Not tried** |

---

## Appendix B: In-Process SPDY Pusher Prototype Tool (`test_pusher.go`)

The following standalone Go program was developed during the initial investigation to empirically prove that an in-process push to the registry pod is feasible without opening local TCP listeners. It was compiled with Go 1.27.4 using `k8s.io/client-go` v0.37.2, connected to a local Kind cluster created with Kind v0.33.0 (`kindest/node:v1.37.0`). (As noted in Section 2.1 and Section 3.1, this SPDY stream mechanism was subsequently superseded by the simpler API server service proxy transport in `ap/pkg/registrypush`).

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

type spdyConn struct {
	httpstream.Stream
	streamConnection httpstream.Connection
}

func (c *spdyConn) LocalAddr() net.Addr                { return &net.IPAddr{} }
func (c *spdyConn) RemoteAddr() net.Addr               { return &net.IPAddr{} }
func (c *spdyConn) SetDeadline(t time.Time) error      { return nil }
func (c *spdyConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *spdyConn) SetWriteDeadline(t time.Time) error { return nil }

func main() {
	kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		panic(err)
	}

	transport, upgrader, err := spdy.RoundTripperFor(config)
	if err != nil {
		panic(err)
	}

	reqURL, err := url.Parse(fmt.Sprintf("%s/api/v1/namespaces/in-cluster-image-registry-system/pods/in-cluster-image-registry-0/portforward", config.Host))
	if err != nil {
		panic(err)
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", reqURL)
	streamConnection, protocol, err := dialer.Dial(portforward.PortForwardProtocolV1Name)
	if err != nil {
		panic(fmt.Sprintf("dial error: %v (protocol: %v)", err, protocol))
	}
	defer streamConnection.Close()
	fmt.Printf("Connected via SPDY protocol %s\n", protocol)

	var requestID atomic.Uint32

	dialContext := func(ctx context.Context, network, addr string) (net.Conn, error) {
		reqID := requestID.Add(1)
		headers := http.Header{}
		headers.Set(corev1.StreamType, corev1.StreamTypeError)
		headers.Set(corev1.PortHeader, "5000")
		headers.Set(corev1.PortForwardRequestIDHeader, strconv.FormatUint(uint64(reqID), 10))

		errorStream, err := streamConnection.CreateStream(headers)
		if err != nil {
			return nil, err
		}
		_ = errorStream.Close()

		headers.Set(corev1.StreamType, corev1.StreamTypeData)
		dataStream, err := streamConnection.CreateStream(headers)
		if err != nil {
			return nil, err
		}

		return &spdyConn{Stream: dataStream, streamConnection: streamConnection}, nil
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: dialContext,
		},
	}

	// Verify registry connectivity
	resp, err := httpClient.Get("http://registry/v2/")
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	fmt.Printf("Registry /v2/ response code: %d\n", resp.StatusCode)

	// Read OCI layout from specified directory
	ociDir := "/tmp/test-oci"
	indexData, err := os.ReadFile(filepath.Join(ociDir, "index.json"))
	if err != nil {
		panic(err)
	}

	var index struct {
		Manifests []struct {
			Digest    string `json:"digest"`
			MediaType string `json:"mediaType"`
			Size      int64  `json:"size"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(indexData, &index); err != nil {
		panic(err)
	}

	manifestDigest := index.Manifests[0].Digest
	fmt.Printf("Found manifest digest: %s\n", manifestDigest)

	manifestPath := filepath.Join(ociDir, "blobs", "sha256", strings.TrimPrefix(manifestDigest, "sha256:"))
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		panic(err)
	}

	var manifest struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		panic(err)
	}

	repoName := "testapp"

	pushBlob := func(digest string) {
		hex := strings.TrimPrefix(digest, "sha256:")
		blobPath := filepath.Join(ociDir, "blobs", "sha256", hex)
		blobData, err := os.ReadFile(blobPath)
		if err != nil {
			panic(err)
		}

		// HEAD check: skip blobs already present in the registry
		headResp, err := httpClient.Head(fmt.Sprintf("http://registry/v2/%s/blobs/%s", repoName, digest))
		if err == nil && headResp.StatusCode == http.StatusOK {
			fmt.Printf("Blob %s already exists (HEAD 200)\n", digest[:19])
			return
		}

		postResp, err := httpClient.Post(fmt.Sprintf("http://registry/v2/%s/blobs/uploads/", repoName), "", nil)
		if err != nil {
			panic(err)
		}
		defer postResp.Body.Close()
		location := postResp.Header.Get("Location")
		if location == "" {
			panic("no Location header in POST blobs/uploads")
		}

		separator := "?"
		if strings.Contains(location, "?") {
			separator = "&"
		}
		uploadURL := location
		if !strings.HasPrefix(uploadURL, "http") {
			uploadURL = "http://registry" + location
		}
		uploadURL += separator + "digest=" + digest

		putReq, err := http.NewRequest(http.MethodPut, uploadURL, bytes.NewReader(blobData))
		if err != nil {
			panic(err)
		}
		putReq.Header.Set("Content-Type", "application/octet-stream")
		putResp, err := httpClient.Do(putReq)
		if err != nil {
			panic(err)
		}
		defer putResp.Body.Close()
		if putResp.StatusCode != http.StatusCreated {
			body, _ := io.ReadAll(putResp.Body)
			panic(fmt.Sprintf("failed to PUT blob %s: status %d (%s)", digest, putResp.StatusCode, string(body)))
		}
		fmt.Printf("Uploaded blob %s (status 201)\n", digest[:19])
	}

	// Push config blob
	pushBlob(manifest.Config.Digest)

	// Push layer blobs
	for _, l := range manifest.Layers {
		pushBlob(l.Digest)
	}

	// Push manifest
	putManifestReq, err := http.NewRequest(http.MethodPut, fmt.Sprintf("http://registry/v2/%s/manifests/v1", repoName), bytes.NewReader(manifestData))
	if err != nil {
		panic(err)
	}
	putManifestReq.Header.Set("Content-Type", index.Manifests[0].MediaType)
	putManifestResp, err := httpClient.Do(putManifestReq)
	if err != nil {
		panic(err)
	}
	defer putManifestResp.Body.Close()
	if putManifestResp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(putManifestResp.Body)
		panic(fmt.Sprintf("failed to PUT manifest: status %d (%s)", putManifestResp.StatusCode, string(body)))
	}
	fmt.Printf("Manifest successfully pushed as v1 (status 201)!\n")
}
```
