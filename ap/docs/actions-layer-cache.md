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

Adopt `actions/cache` with BuildKit `type=local` (configured with
`compression=uncompressed` and save scoped strictly to the default branch). It
is the only mechanism that satisfies the requirement that cache credentials
never reach code executed by a pull request.

BuildKit's native `type=gha` backend cannot meet this requirement: reading from
the cache API requires `ACTIONS_RUNTIME_TOKEN`, which must be supplied to the
build step. Because that step executes the presubmit script
(`dev/ci/presubmits/ap-build`) and workspace code from the pull request's own
tree, untrusted PR code can read the token. While GitHub scopes PR writes to
`refs/pull/<PR>/merge`, PR writes still count against the repository's 10 GB
quota and can evict canonical entries via LRU. Adopting `type=gha` would require
formally relaxing the security requirement to permit PR-scoped token exposure
during presubmits.

Under `actions/cache` with `type=local`, cache is restored in a dedicated action
step before the presubmit script runs and saved in an action step after; the
presubmit script and PR code receive zero credentials. In return, it requires
~8.0–10.6 GB of runner disk space during save on multi-gigabyte images and
restores as a monolithic archive.

## 1. Credential Distribution and Scoping

GitHub exposes `ACTIONS_RUNTIME_TOKEN` alongside `ACTIONS_RESULTS_URL` (Cache
Service v2) and `ACTIONS_CACHE_URL` (Cache Service v1) only to action runtimes,
never to bare `run:` steps [tried: gke-labs/generation-ai#752]. BuildKit's `gha`
backend targets the Actions Results Service (`ACTIONS_RESULTS_URL`) on modern
runners and falls back to `ACTIONS_CACHE_URL` [tried: local buildx v0.38.1
strings inspection; not tried in live workflow]. Support requires BuildKit
v0.13+ or buildx v0.13+; `ap` pins `docker-buildx` v0.38.1 [tried: local buildx
v0.38.1].

Four mechanisms exist to manage these credentials:

- **`actions/cache` with BuildKit `type=local` (Meets Requirement: Zero
  Credentials in PR Code)**: Evaluated first. The generated workflow invokes
  `actions/cache/restore` before the build and `actions/cache/save` on push to
  the default branch. BuildKit reads cache via `--cache-from
  type=local,src=<dir>` and writes via `--cache-to
  type=local,dest=<dir>,mode=min|max,compression=uncompressed` [tried: local
  buildx v0.38.1].
  - *Who reads token*: No process in the job environment ever sees credentials.
    GitHub's runner passes the token internally to `actions/cache` [not tried:
    live workflow].
  - *Scoping & cleanup*: Inherently isolated. Pull request runs omit the save
    step entirely, preventing any PR script from calling the cache API [not
    tried: live workflow].
  - *Third-party publisher*: None; uses official `actions/cache`.
  - *`mode=min` vs. `mode=max` in multi-stage builds*: `mode=min` exports only
    the layers of the final image. Intermediate build stages (e.g., compiling
    native C++/CUDA kernels or building dependencies in builder stages) are not
    cached. For images with heavy compilation stages, such as `vxpu-executor` in
    gke-labs/generation-ai#752 where native compilation accounts for most of the
    4–5 min build, `mode=min` saves very little time, failing to match the
    562 s → 176 s saving measured with `type=gha` (which used `mode=max`).
    Caching those intermediate stages requires `mode=max`.
  - *Disk measurements (3.1 GB final image)*:
    - `mode=min, compression=uncompressed`: Cache directory takes 3.1 GB on
      disk (~15 s export). `actions/cache` compresses this to ~1.8 GB (~30 s)
      [tried: local buildx v0.38.1 and tar]. Total disk footprint during save:
      3.1 GB (daemon) + 3.1 GB (layout) + 1.8 GB (tar) = 8.0 GB.
    - `mode=max, compression=uncompressed`: Intermediate builder layers add ~1.0
      GB [tried: local buildx v0.38.1 multi-stage benchmark]. The uncompressed
      cache layout is ~4.1 GB, and the tar archive is ~2.4 GB. Total disk
      footprint during save: 4.1 GB (daemon) + 4.1 GB (layout) + 2.4 GB (tar) =
      ~10.6 GB. On a runner with ~14 GB free space, 10.6 GB leaves ~3.4 GB
      headroom.
- **Small Action in this Repository (`gke-labs-infra/actions/cache-token`)**:
  Extracts runtime credentials from the action context and provides them as
  masked step outputs to the build step [not tried: action implementation].
  - *Who reads token*: In push to default branch, trusted code holds the token.
    In pull requests, the presubmit script (`dev/ci/presubmits/ap-build`) and
    any tools invoked from the PR tree run inside that step and *can* read
    `ACTIONS_RUNTIME_TOKEN`. Step outputs keep the token from *later* steps, but
    cannot isolate it from code executed in the build step itself.
  - *Scoping & cleanup*: The token cannot be stripped by `ap` because BuildKit
    needs it in its process environment to authenticate HTTP cache requests.
  - *Third-party publisher*: None; maintained in this repository.
- **Third-Party Action (`crazy-max/ghaction-github-runtime`)**: Injects
  credentials into `$GITHUB_ENV` [tried: gke-labs/generation-ai#752]. Every
  step in the job inherits credentials, exposing them to subsequent steps unless
  manually cleared, and adds an external publisher [tried: gke-labs/generation-
  ai#752].
- **First-Party Action Exporting Credentials to `$GITHUB_ENV`**: GitHub does not
  provide one because exporting raw runtime tokens to user scripts breaches
  runner trust boundaries [not tried].

## 2. Read in Pull Requests, Write Only on Default Branch

- **Workflow Enforcement**: The workflow guards save steps and `--cache-to` with:
  `github.event_name == 'push' && github.ref == format('refs/heads/{0}',
  github.event.repository.default_branch)` [tried: gke-labs/generation-ai#752;
  dynamic default branch not tried in workflow]. Pull requests and merge groups
  pass only restore flags (`--cache-from` or `actions/cache/restore`).
- **`ap` CLI Defense-in-Depth**: `ap build` inspects `GITHUB_EVENT_NAME` and
  `GITHUB_REF`, comparing against `repository.default_branch` parsed from
  `$GITHUB_EVENT_PATH`. If not a push to the default branch, `ap` omits
  `--cache-to` regardless of environment flags [not tried: ap implementation].
- **Fork Pull Requests**: Fork PRs run with `github.event_name == 'pull_request'`
  and refs `refs/pull/<PR>/merge`. Cache save is omitted [tried: gke-labs/
  generation-ai#752].
- **What GitHub Cache Scoping Guarantees**: A PR can read cache entries created
  on the default branch. The default branch can *never* read cache entries
  written by a pull request ref (`refs/pull/*`) [not tried: live test].
- **What GitHub Cache Scoping Does NOT Guarantee**:
  - *Quota and LRU Eviction*: The 10 GB limit is repository-wide. If PR code
    obtains `ACTIONS_RUNTIME_TOKEN` and writes cache entries to its PR ref,
    those entries count toward the 10 GB quota and can evict canonical entries
    from the default branch under GitHub's LRU policy [tried: gke-labs/
    generation-ai#752].
  - *Concurrent Write Collisions*: Parallel jobs on push to the default branch
    saving the same key collide with "unable to reserve cache" [tried: gke-labs/
    generation-ai#752]. Only `ap-build` saves; `ap-e2e-*` jobs strictly restore.

## 3. Cost of a Cache Miss, Cost of a Hit, and Layer Selection

- **Miss Cost in Pull Requests**:
  - With `actions/cache` + `type=local`: Restore probe is a single HTTP GET
    manifest check (~1–2 s). On miss, no archive is downloaded and no cache
    flags are passed; the build runs at baseline duration [not tried: CI].
  - With `type=gha`: BuildKit queries the cache API over HTTP. In gke-labs/
    generation-ai#752 (run 44818593546), a PR miss added +106 s (349 s vs 243 s
    baseline; job total 21 min 8 s vs 12 min 53 s) because the job initialized a
    separate `docker-container` builder. Using the host Docker daemon's native
    BuildKit eliminates container startup, reducing miss overhead to the cache
    probe (~2–5 s) [not tried in CI].
- **Miss Cost on Default Branch (Push)**:
  - With `type=gha`: Exports 3.1 GB of layers over HTTP. In gke-labs/generation-
    ai#752 (run 44811610882), export took 562 s vs 243 s baseline (+319 s). On a
    subsequent cache hit (run 44815333020), build time dropped to 176 s (-67 s
    vs baseline, -173 s vs miss) [tried: gke-labs/generation-ai#752].
  - With `actions/cache` + `type=local`: Local export takes ~15 s, tarring
    takes ~30 s, and upload takes ~25 s, totaling ~70 s overhead on push [tried:
    local buildx v0.38.1 and tar; not tried in CI].
- **Cost of a Hit and Break-Even**: With `actions/cache` + `type=local`, a cache
  hit restores one archive every run (~60 s total: ~25 s download, ~15 s
  extract, ~20 s BuildKit import [tried: local buildx v0.38.1 and tar; not tried
  in CI]). A build must take longer than ~60 s from scratch for restoring to pay,
  and the duration threshold for saving must be set above it (e.g., > 90 s) so
  fast builds are not slowed down.
- **Layer Selection Without Dockerfile Parsing**:
  Regex matching on Dockerfile instructions (`pip install`, `cuda`, `cmake`) is
  brittle and misclassifies custom build steps [not tried]. Two non-parsing
  alternatives exist:
  1. *Build Duration Threshold (Recommended)*: Always restore (`--cache-from`).
     Save (`--cache-to`) on the default branch only if the build that just ran
     took longer than a threshold (e.g., > 90 s, above the ~60 s restore
     break-even). Fast builds skip cache export, avoiding upload overhead and
     preserving quota [not tried: ap implementation].
  2. *Export Size Threshold*: Save only if the built image or exported layers
     exceed a minimum size (e.g., > 500 MB) [not tried: ap implementation].

## 4. Environment Detection and Logic Placement

- **Detection**: `ap` detects GitHub Actions via `os.Getenv("GITHUB_ACTIONS") ==
  "true"`. On workstations and other CIs, caching flags are omitted [tried:
  local buildx v0.38.1].
- **Placement of Logic**:
  - *Generated Workflow*: Declares cache steps/actions, passes default branch
    conditions, and restricts export to the primary build job (`ap-build`) [not
    tried: generator update].
  - *`ap build` CLI*: Measures build duration, applies save thresholds, sets
    BuildKit cache flags (`--cache-from`, `--cache-to`), and verifies event refs
    before saving [not tried: ap implementation].

## 5. Visibility and the 10 GB Limit

- **Structured Logging**: `ap` emits structured logs via `scoped.Log(ctx).Info`:
  - Restore: `"restored layer cache", "image", name, "hit", true, "duration", d`
  - Save: `"saved layer cache", "image", name, "scope", name, "duration", d`
  - Miss: `"layer cache miss", "image", name, "reason", "not found"`
- **10 GB Limit**: GitHub evicts entries via LRU across the entire repository
  [tried: gke-labs/generation-ai#752]. When GitHub tokens are present, `ap`
  queries `/repos/{owner}/{repo}/actions/cache/usage` and warns when usage
  exceeds 8 GB (80%): `"actions cache near capacity", "usage", u, "limit", "10GB"`
  [not tried: ap implementation].

## 6. Consumer Workflow and Generated Changes

- **Consumer Experience**: Zero manual edits. Running `ap generate //...` updates
  `.github/workflows/ci-presubmits.yaml`.
- **Workflow Changes**: Under `actions/cache` + `type=local`, `ap generate` emits
  `actions/cache/restore` and `actions/cache/save` steps around the build. Under
  `type=gha`, it emits the credential step and step-scoped environment variables
  [not tried: generator update].

## Decisions for the Maintainer

1. **Adopt Zero-Credential Mechanism or Relax Requirement**: Adopt
   `actions/cache` + `type=local` (recommended: the only option where pull
   request code never holds cache credentials, accepting ~8.0–10.6 GB disk
   footprint during save and monolithic restores), or formally relax the
   security requirement to allow `type=gha` (exposing PR-scoped runtime tokens
   to presubmit scripts in exchange for per-layer network streaming and zero host
   disk footprint).
2. **Multi-Stage Cache Mode for `type=local`**: Choose `mode=min` (safer ~8.0 GB
   save disk footprint, but does not cache intermediate compiler stages) vs.
   `mode=max` (caches intermediate stages like native compilation to achieve
   speedup, but consumes ~10.6 GB disk during save).
3. **Duration Threshold vs. Heuristics**: Set the save duration threshold above
   the ~60 s restore break-even (e.g., > 90 s, no Dockerfile parsing) vs. static
   Dockerfile instruction matching.
4. **Dedicated Exporter Job**: Confirm that only `ap-build` saves cache on push
   to the default branch, while all other jobs (`ap-e2e-*`) strictly restore.
