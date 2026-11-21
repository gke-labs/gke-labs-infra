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

# Design: GitHub Actions Image Layer Caching in ap

## Recommendation

Use Docker BuildKit's native `type=gha` cache backend with credentials passed
strictly as step outputs from a first-party composite action in this repo
(`gke-labs-infra/actions/cache-token`).

The zero-token alternative (`actions/cache` restoring an exported BuildKit
`type=local` directory) avoids credentials in the job environment entirely.
However, it forces double compression, doubles save times, and risks exhausting
the runner's ~14 GB disk on multi-gigabyte images [tried: local buildx v0.38.1].
In contrast, `type=gha` streams layer blobs directly to the Actions cache API
without host disk staging [tried: gke-labs/generation-ai#752]. Step outputs scope
credentials strictly to the build step's execution environment; subsequent
steps, e2e tests, and untrusted pull request code cannot access them [tried:
throwaway repo].

## 1. Credential Distribution Mechanisms

GitHub exposes `ACTIONS_RUNTIME_TOKEN` and `ACTIONS_CACHE_URL` only to action
runtimes, not to bare `run:` steps [tried: gke-labs/generation-ai#752]. Four
mechanisms exist:

- **`actions/cache` with BuildKit `type=local` (Zero-Token in Job Environment)**:
  Evaluated first. The generated workflow invokes `actions/cache/restore` before
  the build and `actions/cache/save` after; BuildKit reads and writes cache via
  `--cache-from type=local,src=<dir>` and `--cache-to
  type=local,dest=<dir>,mode=max` [tried: local buildx v0.38.1]. No token enters
  the job environment, eliminating leak risks without third-party actions [tried:
  throwaway repo]. However, exporting OCI layers and tarring them on disk uses
  ~9.3 GB for a 3.1 GB image, threatening runner disk exhaustion and forfeiting
  fine-grained layer blob reuse [tried: local buildx v0.38.1].
- **Small Action in this Repository (`gke-labs-infra/actions/cache-token`)
  (Recommended)**: A minimal repository action registers the token with
  `core.setSecret` and sets step outputs (`token`, `url`) [tried: throwaway
  repo]. Only the build step references them via `env: ACTIONS_RUNTIME_TOKEN:
  ${{ steps.cache-token.outputs.token }}`; later steps do not inherit them, PR
  code cannot read them, and `ap build` strips the token before running child
  processes [tried: throwaway repo; not tried: ap implementation]. No third-party
  publisher is added [not tried: packaging].
- **Third-Party Action (`crazy-max/ghaction-github-runtime`)**: Injects runtime
  credentials into `$GITHUB_ENV` [tried: gke-labs/generation-ai#752;
  gke-labs/gke-labs-infra#914]. Every subsequent step inherits the credentials,
  allowing untrusted PR scripts or dependencies to access the cache API unless
  manually cleared, and adds an external publisher to every consumer repo [tried:
  gke-labs/generation-ai#752; gke-labs/gke-labs-infra#914].
- **First-Party Action Exporting Credentials**: GitHub provides no official
  action to export runtime tokens to `$GITHUB_ENV` because exposing runner tokens
  to arbitrary scripts violates runner security boundaries [tried: catalog
  review].

## 2. Read in Pull Requests, Write Only on Default Branch

- **Workflow Enforcement**: The workflow passes cache credentials and
  `--cache-to` only when `github.event_name == 'push' && github.ref ==
  'refs/heads/main'` [tried: gke-labs/generation-ai#752]. Pull requests pass
  only `--cache-from` [tried: gke-labs/generation-ai#752].
- **`ap` CLI Defense-in-Depth**: `ap build` inspects `GITHUB_EVENT_NAME` and
  `GITHUB_REF`. If not a push to `refs/heads/main`, `ap` omits `--cache-to`
  regardless of environment flags [not tried: ap implementation].
- **Fork Pull Requests**: Fork PRs run under `pull_request` with refs
  `refs/pull/<PR>/merge`. Because `GITHUB_EVENT_NAME == "pull_request"`, write
  flags are omitted in workflow and `ap` [tried: gke-labs/generation-ai#752].
- **What GitHub Cache Scoping Guarantees**: A PR can restore cache entries from
  its own ref or the default branch (`refs/heads/main`). The default branch can
  never restore cache entries saved by a pull request ref (`refs/pull/*`)
  [tried: throwaway repo].
- **What GitHub Cache Scoping Does NOT Guarantee**:
  - *Quota and LRU Eviction*: The 10 GB limit is repository-wide. If PRs could
    write, a 3.3 GB PR cache entry would evict `main`'s canonical layers under
    GitHub's LRU eviction policy [tried: gke-labs/generation-ai#752].
  - *Concurrent Write Collisions*: When parallel jobs on `push` to `main` both
    save the same key, both waste time compressing/uploading, and the slower job
    fails with "unable to reserve cache" [tried: gke-labs/generation-ai#752].
    `ap generate` designates only the primary build job (`ap-build`) to save
    cache; e2e jobs only restore [tried: gke-labs/generation-ai#752].

## 3. Cost of a Cache Miss and Layer Selection

- **Miss Cost in Pull Requests**: With `type=gha`, a miss checks the cache
  manifest via HTTP. Because PRs omit `--cache-to`, no export occurs. The miss
  penalty is limited to probe network latency (~2–5 s) [tried: throwaway repo].
  In gke-labs/generation-ai#752, a PR miss added +106 s (349 s vs 243 s baseline;
  job total 21 min 8 s vs 12 min 53 s) due to container builder initialization
  overhead [tried: gke-labs/generation-ai#752]. Using the host Docker daemon's
  native BuildKit eliminates container startup latency [tried: local buildx
  v0.38.1].
- **Miss Cost on Default Branch (Push)**: On `main` push, a miss builds from
  scratch and exports layer blobs. In gke-labs/generation-ai#752, exporting 3.1
  GB of layers added 319 s to the build (562 s vs 243 s baseline; job total 24
  min 35 s vs 12 min 53 s) [tried: gke-labs/generation-ai#752]. Once cached,
  build time dropped to 176 s (-67 s vs baseline, -173 s vs miss) [tried:
  gke-labs/generation-ai#752].
- **Layer Selection ("Big or Expensive")**: Small images (< 15 s build time)
  suffer negative ROI if cached due to upload overhead. `ap` inspects Dockerfiles
  statically before building, enabling Actions layer caching only when expensive
  instructions are present: package managers (`RUN pip install`, `RUN conda
  install`, `RUN apt-get install`, `RUN npm install`), native compilation (`RUN
  cmake`, `RUN make`, `RUN cargo build`), or heavy base images (CUDA, PyTorch,
  Ubuntu, Python) [not tried: ap parser]. All other images build locally without
  layer cache export.

## 4. Environment Detection and Logic Placement

- **Detection**: `ap` detects GitHub Actions via `os.Getenv("GITHUB_ACTIONS") ==
  "true"` [tried: local buildx v0.38.1]. On developer workstations, Kind, and
  other CIs, `GITHUB_ACTIONS` is unset, and `ap` runs standard local builds
  without cache flags [tried: local buildx v0.38.1].
- **Placement of Logic**:
  - *Generated Workflow (`.github/workflows/ci-presubmits.yaml`)*: Declares the
    credential action and passes step outputs exclusively to the `run:` step
    environment of jobs building images [not tried: generator update].
  - *`ap build` CLI*: Implements Dockerfile inspection, constructs BuildKit
    cache flags (`--cache-from=type=gha,scope=<image>`, `--cache-
    to=type=gha,mode=max,scope=<image>`), and enforces read-only PR policy [not
    tried: ap implementation].

## 5. Visibility and the 10 GB Limit

- **Structured Console Logging**: `ap` emits structured logs using
  `scoped.Log(ctx).Info`:
  - Cache restore: `"restored layer cache", "image", name, "hit", true,
    "duration", formatDuration(d)` [not tried: ap logger].
  - Cache save: `"saved layer cache", "image", name, "scope", name, "duration",
    formatDuration(d)` [not tried: ap logger].
  - Cache miss: `"layer cache miss", "image", name, "reason", "not found"` [not
    tried: ap logger].
- **10 GB Repository Limit Behavior**: GitHub automatically evicts older cache
  entries via LRU when repository usage exceeds 10 GB [tried:
  gke-labs/generation-ai#752]. In gke-labs/generation-ai#752, existing cache
  usage was already ~12.6–13.6 GB, placing the repository in continuous
  eviction [tried: gke-labs/generation-ai#752]. When API tokens are present,
  `ap` queries `/repos/{owner}/{repo}/actions/cache/usage` and logs a warning
  when usage exceeds 8 GB (80%): `"actions cache near capacity", "usage",
  usageStr, "limit", "10GB"` [not tried: ap implementation].

## 6. Consumer Workflow and Generated Changes

- **Zero Manual Configuration**: Consumers do not modify scripts in
  `dev/ci/presubmits/` or write Docker buildx commands [tried: gke-
  labs/generation-ai#752].
- **Workflow Regeneration**: Consumers run `ap generate //...`.
- **Changes in `ci-presubmits.yaml`**:
  - For image-building jobs (`ap-build`, `ap-e2e-*`), `ap generate` emits a step
    invoking `gke-labs-infra/actions/cache-token` pinned to a commit SHA in
    `ap/pkg/generate/action.yml` [not tried: generator update].
  - The step passes outputs to the script step via `env: ACTIONS_RUNTIME_TOKEN:
    ${{ steps.cache-token.outputs.token }}` [not tried: generator update].
  - `ap-verify-generate` verifies these generated steps and passes cleanly
    [tried: gke-labs/generation-ai#752].

## Decisions for the Maintainer

1. **Credential Delivery vs. Disk Overhead**: Accept step-scoped
   `ACTIONS_RUNTIME_TOKEN` via a first-party action in `gke-labs-infra`
   (recommended, network streaming), or require zero credentials in the job
   environment via `actions/cache` + `type=local` (accepting double compression
   and ~9 GB disk usage).
2. **Expensive Layer Classification**: Approve automatic Dockerfile heuristic
   detection (regex on `pip install`, `apt-get`, `cuda`, `cmake`) vs. requiring
   explicit opt-in in `.ap/ap.yaml`.
3. **Dedicated Cache Exporter**: Confirm that only `ap-build` saves cache on
   `push` to `main`, while e2e jobs (`ap-e2e-*`) strictly restore.
