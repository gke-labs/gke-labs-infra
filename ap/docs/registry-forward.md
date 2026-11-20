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

# Local Registry Push Architecture

`ap` pushes locally built container images to the in-cluster registry
(`in-cluster-image-registry`) without opening any local TCP listeners on the
developer workstation.

## 1. What Happens on a Local Push to `images.local`

`ap` takes this build path during `ap build --push` or `ap deploy` when all of
the following conditions are met:
- Image pushing is requested (`push = true`).
- The image repository prefix is the default `images.local`.
- The target cluster is not a Kind cluster (`!tc.IsKind()`).
- No external BuildKit daemon is configured (`--buildkit-host` is omitted).
- `ap` is not running inside a cluster container (`!naming.InCluster`).

When these conditions do not match, `ap` routes image management through other
mechanisms instead:
- **Kind clusters**: `ap deploy` loads images directly into the Kind container
  node image store via `KindLoadTask`, using `docker save` and `ctr images import`.
  No registry push is performed.
- **In-cluster BuildKit (`--buildkit-host k8s` or `k8s://...`)**: `buildctl`
  connects over a private Unix domain socket via `BuildkitForwarder`, and the
  remote BuildKit daemon pushes directly to the in-cluster registry via internal
  cluster DNS (`in-cluster-image-registry.in-cluster-image-registry-system.svc.cluster.local:80`).
- **In-cluster execution**: When running inside a cluster, `ap` builds images
  and pushes directly using internal cluster DNS.
- **External registries (`IMAGE_PREFIX != "images.local"`)**: `ap` builds and
  pushes directly to the designated remote registry endpoint over HTTPS.

For a local push to `images.local`, `ap` executes three sequential steps:

1. **Build with Containerized BuildKit**:
   `ap` builds images using a BuildKit daemon running inside the local
   `ap-builder` Docker container (`buildx_buildkit_ap-builder0`), ensured via
   `ensureBuildkitContainer`. The host `buildctl` binary communicates with the
   container using `buildctl --addr docker-container://buildx_buildkit_ap-builder0`,
   multiplexing gRPC commands over container standard I/O (`docker exec`).

2. **OCI Layout Directory Export and Pruning**:
   BuildKit exports the build result directly to an uncompressed OCI layout
   directory on the host filesystem using `--output type=oci,tar=false,dest=<dir>`.
   The directory is located under `AP_CACHE_DIR/images/<repository hash>/<image>`,
   where `AP_CACHE_DIR` defaults to the user cache directory and `<repository hash>`
   is a 12-character SHA-256 hash of the repository root path. Immediately
   following each build, `pruneOCIDirectory` runs `layoutPath.GarbageCollect()` and
   `layoutPath.RemoveBlob()` to delete unreferenced blobs from previous builds,
   keeping disk usage bounded while retaining cached layers.

3. **In-Process Push via API Server Service Proxy (`ap/pkg/registrypush`)**:
   `ap` reads the OCI layout directory and pushes the active layers and manifest
   to the registry in-process using `registrypush.NewTransport` and
   `registrypush.Push`. Requests route directly through the Kubernetes API server's
   service proxy subresource:
   `/api/v1/namespaces/in-cluster-image-registry-system/services/in-cluster-image-registry:5000/proxy`
   Blobs the registry already has are not uploaded again (go-containerregistry
   does this); missing layer and config blobs are uploaded, followed by the
   manifest.

Throughout this process, no process listens on a local TCP port on the
developer workstation. This eliminates port collisions (such as conflicts with
macOS AirPlay on port 5000), avoids exposing registry endpoints to local browsers
or background processes, and eliminates the secondary `alpine/socat` proxy
container previously required on macOS.

## 2. What Users Notice and Need

- **BuildKit Cache vs. Docker Cache**: Builds rely on BuildKit's internal cache,
  which lives in the BuildKit container's volume rather than the host Docker
  daemon's storage. The first build after container creation is cold, but
  subsequent builds leverage BuildKit layer caching.
- **Image Not in `docker images`**: Because images are exported directly to an
  OCI directory and pushed via the Kubernetes API server, built images do not
  appear in the host Docker daemon image list (`docker images`).
- **RBAC Permissions**: Users must have `services/proxy` permission on the
  `in-cluster-image-registry` service in the `in-cluster-image-registry-system`
  namespace. This replaces the `pods/portforward` permission required by the
  legacy port-forward implementation.
- **Container Startup Failure**: If the Docker daemon is stopped or workstation
  security policies disallow privileged containers, `ap` fails fast with an
  explicit error:
  ```
  a BuildKit container is required: failed to start builder ap-builder (ran: ...). Please ensure Docker is running and allows running BuildKit containers (for example, privileged containers must be allowed)
  ```

## 3. Known Limits

- **Platform Validation**: The service proxy push has been tested and benchmarked
  on Kind clusters; it has not been tried against GKE.
- **Per-Request Endpoint Selection**: The Kubernetes API server service proxy
  independently selects an endpoint for each HTTP request. This behaves reliably
  because `in-cluster-image-registry` is deployed as a single-replica StatefulSet
  (`replicas: 1`), ensuring all requests reach the same registry pod.
- **Performance and Recorded Timings**: Recorded when the change landed for
  `autodeploy/examples/helloworld` on kind, comparing the new path against the
  previous one:
  - First build: 20.6 s against 16.5 s.
  - Unchanged rebuild: 0.4 s against 0.5 s.
  - Small change: 3.3 s against 1.4 s (small changes are about 2 s slower than
    before).
  In a standalone probe, two 400 MiB layers were pushed through the service proxy
  in 2.4 s.

## 4. Alternatives Considered

- **Port-forward stream push**: Open an in-process SPDY port-forward connection
  directly to container port 5000 on the registry pod and stream HTTP requests
  over a `net.Conn` wrapper. Dropped due to implementation size and complexity
  (~300 lines of custom SPDY framing, pod discovery, and connection wrapping
  versus ~90 lines of standard HTTP `RoundTripper` delegation).
- **Archive export with `docker save`**: Build into the Docker daemon with
  `--load` and stream `docker save` tarball layers in-process over the service
  proxy. Dropped because tarball export adds an unconditional 2.0–2.4 s latency
  penalty on every build, even for no-op changes.
- **A proxy container**: Run BuildKit and a lightweight proxy container on an
  isolated Docker bridge network, tunneling registry traffic through `ap` over
  container stdio. Dropped due to multi-container lifecycle complexity and
  custom protocol multiplexing overhead.
- **Narrowing the old listener**: Retain `kubectl port-forward` while allocating
  dynamic ephemeral ports, binding explicitly to `127.0.0.1`, and scoping the
  listener strictly to the push phase. Dropped because it still leaves an open
  TCP port on the host and requires a secondary `socat` helper container on macOS
  due to Docker VM network isolation.

The full investigation, including the prototype code, is in this file's git history.
