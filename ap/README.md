# ap

`ap` is a development automation tool for gke-labs projects. It handles tasks like testing, building, deploying, formatting, and generating code.

## Configuration

`ap` is configured via files in the `.ap/` directory. By default, it looks for this directory in the current directory and walks up the tree until it finds one.

### Repository Root and Subtrees

The repository root (the directory containing `.git`) is the only root. Configuration is read from `<repo>/.ap/`.

Commands operate on a subtree of the repository: either the positional target where one is provided (e.g. `ap e2e autodeploy`, `ap deploy <dir>`), the subtree of the current working directory, or the whole repository when run from the root. For `ap build`, positional targets can also be image directories (`<parent>/images/<name>`); see `build` below.

### Subtree Configuration Overrides

A `.ap/` directory in a subtree can override configuration options for files beneath it. The effective configuration for a path is the base configuration at `<repo>/.ap/` with keys overridden by the nearest enclosing subtree `.ap/go.yaml`, walking up to the root. Only keys present in a subtree file override; absent keys inherit, and lists such as `skip` replace rather than merge. An explicitly written key always wins (for example, `mode: ""` or `toolchain: ""` in a subtree file overrides rather than being treated as unset), and mapping sections (such as `gofmt:`) merge per key instead of replacing the whole section. Overrides apply to per-module and per-directory settings (`gofmt`, `govet`, `govulncheck`, `tidy`, `skip`, `lint` analyzer modes and baselines, and `toolchain`), while tool version files (`protoc.yaml`, `docker.yaml`, `buildctl.yaml`, `helm.yaml`, `kind.yaml`, `kubectl.yaml`, `kustomize.yaml`) and `.ap/ap.yaml` remain root-only. A subtree `.ap/` directory does not change discovery, build context, image naming, or generated presubmits. For example, to disable `govulncheck` in a subtree, create a `.ap/go.yaml` in that subtree:
```yaml
govulncheck:
  enabled: false
```

### Environment Variables

You can configure `ap` behavior by setting the following environment variables:
- `AP_CACHE_DIR`: Explicitly sets the root cache directory (defaults to `~/.cache/ap` or the OS equivalent; holds tools/protoc, plugins, and codestyle analysis cache).

## Generation

The `ap generate` command runs all generation tasks across the repository:
- Legacy generator scripts located at `dev/tasks/generate-*` under the selected subtree.
- `go generate ./...` in modules containing `//go:generate` directives.
- Built-in generators for presubmit scripts (`ap-test`, `ap-lint`, `ap-build`, `ap-e2e`, `ap-verify-generate`, and optional `ap-audit-generated`) and the unified GitHub Actions workflow at `.github/workflows/ci-presubmits.yaml`.
- Code formatting and file header stamping across sources (equivalent to running `ap format`).

### Presubmits and CI

The `ap generate` command generates CI presubmit scripts (e.g., `ap-test`, `ap-lint`, `ap-build`, `ap-e2e`) in `dev/ci/presubmits/` and creates a unified GitHub Actions workflow at `.github/workflows/ci-presubmits.yaml`. A task script's job suffix comes from the script's directory relative to the repository root (e.g. `autodeploy/dev/tasks/test-e2e` -> `ap-e2e-autodeploy`).

The generated workflow defines a concurrency group so that a new push to a pull request cancels superseded presubmit runs, while pushes to `main` and merge queue runs are never cancelled.

### Pinned Action Versions

The GitHub Actions used in generated workflows (`actions/checkout`, `actions/setup-go`, `actions/upload-artifact`) are defined in `ap/pkg/generate/action.yml`, pinned to full commit SHAs with `# ratchet:` comments. Dependabot is configured in `.github/dependabot.yml` to monitor both `/` and `/ap/pkg/generate` in a single group, so a single pull request updates both `action.yml` and `.github/workflows/ci-presubmits.yaml` and no manual step is expected; if a Dependabot pull request ever updates only one of the two files, the fix is `ap generate`. When merged, `ap generate` automatically emits the updated action versions in subsequent workflow regenerations across all consuming repositories. Consumers should keep their own `github-actions` cooldown at 2 days or more, so that the generator is updated first.

### Presubmit Artifacts

Presubmit scripts may write anything to `$ARTIFACTS` (set to `/tmp/artifacts` in generated GitHub Actions jobs), and it is uploaded as `artifacts-<job>` upon job completion (even on failure). If no files are written, the upload step completes without producing warnings or errors. When `$ARTIFACTS` is set, `ap` also writes each leaf task's buffered output under `$ARTIFACTS/tasks/` (with subdirectories reflecting the task tree, e.g. `$ARTIFACTS/tasks/lint-repo/go-lints/go-lint-autodeploy/go-vet.log`, and numeric suffixes on name collisions); task names are free-form human-readable strings, and the first line of each log file is the task's human-readable name.

### Image Layer Caching in CI

When building container images inside GitHub Actions (`GITHUB_ACTIONS=true`), `ap` caches image layers in GitHub's Actions cache using a local BuildKit cache directory under the runner's temporary directory (`${{ runner.temp }}/ap-layer-cache`). Layer caches are restored before `ap-build` and saved only on pushes to the default branch after builds complete. Pull requests strictly restore layer caches and cannot write or overwrite cache entries. At export `ap` records per-step durations next to the image cache; on subsequent builds importing that cache, `ap` compares the time saved by cached steps against the cache cost (export time plus restore share), dropping and bypassing unprofitable image caches until their Dockerfile changes. Each image logs an `evaluated layer cache` line with the measured duration saved, duration cost, and whether the cache was kept or dropped. Outside GitHub Actions, layer caching is inactive and behavior is unchanged.


### Output Atomicity and Verification

All generator outputs are written atomically. Generated files carry standard headers such as `# Code generated by ap generate. DO NOT EDIT.` or `// Code generated by ap generate. DO NOT EDIT.`.

Running `ap generate --verify` (or `ap verify-generate`) perturbs each generated file and proves that its generator restores it to its clean state, and covers container plugin generate tasks by running them into staging and comparing outputs against the checkout without modifying the working tree. In addition, `ap generate --audit-generated` (with `--strict`) audits the repository for generated files that lack a standard generated header.

## Deployment

The `ap deploy` command builds container images and applies Kubernetes manifests. Built images deployed with `:latest` are pinned by digest (`tag@sha256`) in applied manifests so redeploying always rolls pods to the new image.

Positional path arguments select what to deploy: `ap deploy <dir>` applies every manifest under any `k8s/` directory beneath `<dir>` (including example manifests), whereas `ap deploy <dir>/k8s/<file>.yaml` applies a single file and builds only the images that file references. `ap deploy <dir>/kustomization.yaml` builds the target with `kustomize build` and applies the generated YAML as a manifest, supporting ApplySet, namespace retargeting, image placeholders, and waiting for established CRDs. A kustomization target builds no container images for now. Positional targets can also be Git URLs (e.g. GitHub web links like `ap deploy https://github.com/org/repo/blob/branch/path` or `https://github.com/org/repo/tree/branch/path`, raw links like `ap deploy https://raw.github.com/org/repo/blob/branch/path`, or Kustomize-style syntax like `ap deploy https://github.com/org/repo//path?ref=branch`), which clone locally into `~/.cache/ap/git` and deploy the target from the clone.

When the current context is a kind cluster, `ap deploy` works with no `IMAGE_PREFIX`, building locally, loading images into the kind cluster using a unique content-addressed tag, and applying. Use `--cluster=kind://<name>` to target a specific kind cluster, or `--cluster=<context>` to target a kubeconfig context. Passing `-n <namespace>` retargets namespaced objects to the target namespace, logging which manifests were retargeted and any cluster-scoped objects applied as-is.

For non-kind clusters, configure building with a remote BuildKit daemon using `--buildkit-host` or the `BUILDKIT_HOST` environment variable (e.g. `k8s://namespace/service`). The shorthand `--buildkit-host k8s` or `BUILDKIT_HOST=k8s` expands to `k8s://autodeploy-system/buildkit`. The in-cluster BuildKit path uses a private unix socket and opens no local port, using the pinned `buildctl`. `IMAGE_PREFIX` defaults to `images.local`, which assumes the in-cluster image registry and its node agent. Pass `--skip-push` to skip pushing built images to the registry.

`ap deploy` applies manifests as written, apart from image references, the `-n` namespace, and opt-in content-hash suffixes; it does not install CRDs that the manifests do not contain, and it does not create Secrets.

Annotate a `ConfigMap` or `Secret` in plain manifests with `infra.labs.gke.io/hash-suffix: "true"` to append a deterministic 10-character SHA-256 content-hash suffix to its name (e.g. `app-config-<hash>`). Any annotation value other than `"true"` is an error. References in pod specs of `Deployment`, `DaemonSet`, `StatefulSet`, `Job`, `CronJob`, and `Pod` in the same namespace (`envFrom`, `env[].valueFrom`, `volumes[]` including `projected.sources[]` and volume `secretRef`, and `imagePullSecrets`) are rewritten to point to the suffixed name so configuration changes trigger a clean Kubernetes rollout. References from other resource kinds (such as `Ingress` TLS secrets, custom resources, or code reading a fixed name) are not rewritten, so only annotate objects consumed by pods; unannotated objects and kustomization targets are left untouched. `ap deploy` labels each suffixed object with `infra.labs.gke.io/hash-suffix-of: <original name>` and, after the apply and workload readiness wait succeed, removes superseded `ConfigMap` or `Secret` objects in the same namespace carrying that label. Nothing is removed if the rollout wait fails, if `--wait=false` is passed, or on `--dry-run`.

For example, to deploy a controller from a single manifest to a non-kind cluster using in-cluster buildkit and registry:

```bash
ap deploy path/to/k8s/controller.yaml -n my-namespace --cluster my-context --buildkit-host k8s
```

Pass `--applyset=[RESOURCE[.GROUP]/]NAME` to track deployments using the KEP-3659 ApplySet specification. `ap deploy` stamps `applyset.kubernetes.io/part-of: <id>` on every applied object and updates the pre-existing parent object with the ApplySet ID label, tooling version (`ap/<version>`), and `contains-group-kinds` (and `additional-namespaces` when applicable) annotations. The deploy fails if the parent does not exist, carries a conflicting tooling annotation, or is namespace-scoped without `--namespace`.

At the end of the rollout, `ap deploy` automatically waits in parallel for all applied Deployments, DaemonSets, and StatefulSets to be ready within a single overall timeout (`--timeout`, defaults to `5m`). On failure or timeout, `ap deploy` details which workloads were not ready, unavailable replicas, and pod container waiting reasons or termination messages. Pass `--wait=false` to skip waiting.

## End-to-End Testing (e2e)

If a root has `tests/e2e` Go tests, `ap e2e` runs `RUN_E2E=1 go test -v -count=1 -timeout 30m ./tests/e2e/...`. A `dev/tasks/test-e2e*` script at the same root runs before them and is for setup only (build a binary, prepare a cluster, free disk space); it must not run the Go tests itself, or they run twice.

## Configuration Files

The following files can be placed in the `.ap/` directory:

### headers.yaml

Configures the `fileheaders` check (part of `ap format`). It ensures files have the correct license and copyright headers.

**Note:** This file was previously named `file-headers.yaml`.

Example `.ap/headers.yaml`:
```yaml
license: apache-2.0
copyrightHolder: Google LLC
skip:
- "third_party/"
- "*.json"
skipGenerated: true
```

### go.yaml

Configures Go-specific tooling and linters.

Example `.ap/go.yaml`:
```yaml
# Top-level paths to skip during repository scanning and linting
skip:
  - "vendor/**"

gofmt:
  enabled: true

tidy:
  enabled: true

govet:
  enabled: true

govulncheck:
  enabled: true

lint:
  # Cross-cutting options across all linters (default: false)
  skipGenerated: false
  skipTests: false
  goos:
    - linux
    - darwin

  # Per-analyzer configurations (mode: error | warn | ignore)
  analyzers:
    unused:
      mode: error
      skipGenerated: true
    unusedparameters:
      mode: error
    testcontext:
      mode: warn
    replaceEmptyInterfaceWithAny:
      mode: warn
    testOnlyImports:
      mode: error
      packages:
        - github.com/google/go-cmp
    closeWriter:
      mode: error
      types:
        - "*bufio.Writer"
    droppedErrors:
      mode: error
      baseline: .ap/droppederrors-baseline.txt
      exclude:
        - fmt.Fprintln
      useDefaultExcludes: true
    deferInLoop:
      mode: error
```

`go.yaml` options:
- `toolchain`: Pinned Go toolchain version (e.g. `toolchain: go1.27.3`). When running `ap tool go`, the version is resolved in the following order:
  1. The `toolchain` line of the nearest `go.mod` when run inside a module.
  2. The `go` line of the nearest `go.mod` when run inside a module.
  3. The `toolchain` setting in `.ap/go.yaml` if configured.
  4. The newest stable release in `ap/pkg/tool/go/versions.yaml`.
- `skip`: An optional list of glob patterns to ignore during repository scanning and linting (e.g. `vendor/**`).
- `tidy.enabled`: Boolean flag (default: `true`). When enabled, `ap generate` and `ap format` run `go mod tidy` alongside `gofmt` for each Go module in scope, and `ap lint` verifies that modules are tidy using `go mod tidy -diff`.
- `lint.goos`: An optional list of target `GOOS` platforms to check (e.g. `[linux, darwin]`), which overrides automatic detection; omit it for auto (automatically detects non-host GOOS targets referenced by in-scope Go files via filename suffixes and `//go:build` constraints).
- `lint.skipGenerated`: Cross-cutting boolean flag. When `false` (the default), generated code is linted unless a linter specifies its own default or per-linter override.
- `lint.skipTests`: Cross-cutting boolean flag. When `false` (the default), test code (`*_test.go`) is linted unless a linter specifies its own default or per-linter override.
- Analyzers and modes: Every check is configured under `lint.analyzers.<name>` and is on at `mode: error` unless configured (except `testcontext` and `replaceEmptyInterfaceWithAny` which default to `mode: warn`, and `unusedparameters` which is opt-in). Valid modes are `error`, `warn`, and `ignore`.
- Per-analyzer options under `lint.analyzers`:
  - `unusedparameters.mode`: Enables parameter checking in the `unused` analyzer (`error | warn | ignore`).
  - `testOnlyImports.packages`: List of package import paths permitted only in test files (`*_test.go`).
  - `closeWriter.types`: List of type names whose instances must check errors when closed.
  - `droppedErrors`: Checks for dropped errors (calls whose returned `error` is ignored). Supports `exclude` (additional functions/methods to exclude), and `useDefaultExcludes` (boolean, defaults to `true` to exclude standard library functions).
  - Baseline ratchet: `baseline:` is accepted on every analyzer section (`lint.analyzers.<check>.baseline: <path>`); the default file is `.ap/<analyzer>-baseline.txt`. Every analyzer uses a one-line format (`file:enclosingFunc:detail`, with the optional category field `droppederrors` writes). Running `ap lint --write-baseline` writes a baseline for each enabled analyzer that has findings (`ap lint droppederrors --write-baseline` still works). Matched findings are suppressed, and stale entries are reported so they can be removed and the baseline ratchets toward zero. This is the way to adopt a check at `mode: error` in a repo with existing findings, instead of `mode: warn`.

### proto.yaml

Configures protobuf compiler (`protoc`) and plugin pinning for `ap tool protoc`.

Example `.ap/proto.yaml`:
```yaml
protoc: "36.2"
plugins:
  protoc-gen-go: "v1.36.12"
  protoc-gen-go-grpc: "v1.6.2"
```

- `protoc`: Pinned version of the `protoc` compiler (e.g. `"36.2"` or `"latest"`).
- `plugins`: Map of protobuf plugin names to pinned version tags (e.g. `protoc-gen-go: "v1.36.12"`).
- Versions listed in `ap/pkg/tool/protoc/versions.yaml` resolve offline with SHA-256 asset digest verification without network round-trips to GitHub.
- Any requested version not found in `versions.yaml` falls back to resolving dynamically from the GitHub API with a warning, which is subject to GitHub API rate limits.
- Run `ap tool update-versions` to refresh the table of well-known tool releases and plugin versions with their asset digests.

### docker.yaml

Configures Docker CLI and docker-buildx pinning for `ap tool docker`, `ap tool docker-buildx`, and `ap deploy`.

Example `.ap/docker.yaml`:
```yaml
docker: "29.9.2"
buildx: "0.38.1"
```

- `docker`: Pinned version of the Docker CLI binary (defaults to the newest version in `versions.yaml`, e.g. `"29.9.2"`).
- `buildx`: Pinned version of the `docker-buildx` binary (defaults to the newest version in `versions.yaml`, e.g. `"0.38.1"`).
- Pinned binaries are downloaded and cached in `~/.cache/ap/tools/`.
- `ap deploy` and `ap build` automatically use these pinned versions rather than whatever version happens to be on the system PATH.

### kind.yaml

Configures `kind` pinning for `ap tool kind` and test harnesses.

Example `.ap/kind.yaml`:
```yaml
kind: "v0.33.0"
```

- `kind`: Pinned version of the `kind` binary (defaults to the newest version in `versions.yaml`, e.g. `"v0.33.0"`).
- Pinned binaries are downloaded and cached in `~/.cache/ap/tools/kind/<tag>/bin/`.
- Test harnesses and `ap tool kind` automatically use this pinned version without requiring a manual download.

### helm.yaml

Configures `helm` pinning for `ap tool helm`.

Example `.ap/helm.yaml`:
```yaml
helm: "v4.3.2"
```

- `helm`: Pinned version of the `helm` binary (defaults to the newest version in `versions.yaml`, e.g. `"v4.3.2"`).
- Pinned binaries are downloaded and cached in `~/.cache/ap/tools/helm/<tag>/bin/`.
- `ap tool helm` automatically uses this pinned version without requiring a manual download.

### kubectl.yaml

Configures `kubectl` pinning for `ap tool kubectl`.

Example `.ap/kubectl.yaml`:
```yaml
kubectl: "v1.37.2"
```

- `kubectl`: Pinned version of the `kubectl` binary (defaults to the newest version in `versions.yaml`, e.g. `"v1.37.2"`).
- Pinned binaries are downloaded and cached in `~/.cache/ap/tools/kubectl/<tag>/bin/`.
- `ap tool kubectl` automatically uses this pinned version without requiring a manual download.

### kustomize.yaml

Configures `kustomize` pinning for `ap tool kustomize`.

Example `.ap/kustomize.yaml`:
```yaml
kustomize: "v5.8.3"
```

- `kustomize`: Pinned version of the `kustomize` binary (defaults to the newest version in `versions.yaml`, e.g. `"v5.8.3"`).
- Pinned binaries are downloaded and cached in `~/.cache/ap/tools/kustomize/<tag>/bin/`.
- `ap tool kustomize` automatically uses this pinned version without requiring a manual download.

### buildctl.yaml

Configures `buildctl` pinning for `ap tool buildctl`.

Example `.ap/buildctl.yaml`:
```yaml
buildctl: "v0.34.2"
```

- `buildctl`: Pinned version of the `buildctl` binary (defaults to the newest version in `versions.yaml`, e.g. `"v0.34.2"`).
- Pinned binaries are downloaded and cached in `~/.cache/ap/tools/buildctl/<tag>/bin/`.
- `ap tool buildctl` automatically uses this pinned version without requiring a manual download.

### Updating Tool Versions

Run `ap tool update-versions` to refresh baked-in release tables (`ap/pkg/tool/protoc/versions.yaml`, `ap/pkg/tool/docker/versions.yaml`, `ap/pkg/tool/buildctl/versions.yaml`, `ap/pkg/tool/helm/versions.yaml`, `ap/pkg/tool/kind/versions.yaml`, `ap/pkg/tool/kubectl/versions.yaml`, `ap/pkg/tool/kustomize/versions.yaml`, and `ap/pkg/tool/go/versions.yaml`) with verified checksums. Flags allow skipping individual tools (all default to `true`): `--protoc`, `--docker`, `--buildx`, `--buildctl`, `--helm`, `--kind`, `--kubectl`, `--kustomize`, and `--go` (e.g. `ap tool update-versions --docker=false` skips the slower Docker tarball downloads).

### Tool Installation and Busybox Dispatch

`ap` can be invoked under the name of any tool it manages (`buildctl`, `helm`, `kind`, `kubectl`, `kustomize`, `docker`, `docker-buildx`, `protoc`, `go`) to behave as `ap tool <name> -- <args...>`. On Unix, `ap` uses `syscall.Exec` of the pinned binary so signals, stdio, and exit codes pass straight through without an intermediate wrapper process. Running `ap tool install [dir]` creates or refreshes symlinks pointing to `ap` for all registered tools in `${REPO_ROOT}/.build/bin` (or `AP_CACHE_DIR/bin` outside a repository) or in a specified directory on `PATH`, giving developers and CI jobs every pinned tool automatically. Before executing any discovered task script (`dev/tasks/test-*`, `test-e2e*`, `build-*`, `generate-*`, etc.), `ap` ensures those symlinks exist in `${REPO_ROOT}/.build/bin` and prepends that directory to `PATH`. Task scripts should call tools by name and never download them; `ap` provides them on `PATH`. Tasks obtain pinned tools through the task context, never by calling the installers.

### ap.yaml

General configuration for `ap` itself.

Example `.ap/ap.yaml`:
```yaml
version: "v0.1.0"
test:
  race: true
presubmits:
  skipIfOnlyChanged:
  - docs/*
  - README.md
generate:
  ignoreOrphans:
    - "legacy/*.pb.go"
  auditPaths:
    - "pkg/**"
    - "cmd/**"
  auditGenerated: true
```

`version` pins the version of `ap` invoked by generated wrappers and workflows.
It accepts `!self` (to run `go run ./ap` within this repository), `latest`
(to float to the latest release), or a valid Go module version string (such as
`v0.1.0` or a pseudo-version like `v0.0.0-20261024111447-4ab120e48572`). Branch
names and commit hashes are rejected on purpose.

`test.race` is an optional boolean flag. When set to `true`, `ap test` runs Go unit
tests under the data race detector (`-race`) by default. This also ensures generated
presubmit jobs (`ap-test`) run tests under the race detector. Pass `--test.race=false` to
bypass the race detector for an individual invocation.

`presubmits.skipIfOnlyChanged` is an optional list of bash case globs
matched against each repo-relative changed path (`*` also matches `/`).
When every file changed by a pull request matches one of the patterns,
the generated presubmit scripts exit early but successfully, so
required status checks stay green without paying for a full run (a
`paths:` filter on the workflow would instead leave required checks
pending forever). The guard fails open — shallow history or a
non-merge HEAD falls through to a full run — and push / merge_group
events always run everything, so a merge queue remains a complete
gate. Only list paths no presubmit output depends on.

`generate.ignoreOrphans` is an optional list of glob patterns to ignore
when verifying generator coverage, for legacy generated files that do not
yet have a generator.

`generate.auditPaths` is an optional list of glob patterns to restrict
candidate files evaluated by `ap generate --audit-generated`. When omitted,
all eligible files across the repository are audited.

`generate.auditGenerated` is a boolean flag. When set to `true`, `ap generate`
generates `dev/ci/presubmits/ap-audit-generated` (which runs
`ap generate --audit-generated --strict`), adding an automated presubmit check
that fails if any generated file lacks a standard generated code header.

### plugins.yaml

Configures plugins for extensible build tooling.

Plugins communicate with `ap` using gRPC over stdio (`ap.plugin/v1alpha1`). When a plugin starts, `ap` discovers capabilities via the `Describe` RPC, streams repository view files as `FileChunk` messages over the bi-directional `RunTask` stream, receives streamed task logs and output chunks, and safely writes outputs via `fileutils.UntrustedRoot`. Files travel over the stream; no mounts are required.

#### Sandbox Backends and Requirements

- **`image:` plugins run through the clix sandbox library**: `ap` launches remote container images through `github.com/gke-labs/clix/sandbox` rather than requiring Docker directly.
- **Backend Auto-Detection**: `ap` automatically detects the sandbox backend to use. It tests backends in a fixed order:
  1. `docker`: Used whenever a Docker engine answers on the host.
  2. `apple-container`: Used on macOS when the `container` CLI is installed.
  3. `chroot`: Used on Linux when user namespaces or root privileges are available.
  4. `proot`: Used on Linux when `proot` is installed on `PATH`.
  - Detection is entirely automatic: `ap` does not read `CLIX_SANDBOX`, and there is no flag or configuration key.
  - If no backend works on the host, `ap` fails with an actionable error listing every backend tried and why each failed.
- **Network Isolation Guarantee**: Plugins default to running offline without network access (`network: none` in the sandbox). Where a backend cannot guarantee network isolation, the run fails with an error naming the plugin and the backend reason.
- **`dockerfile:` plugins require Docker**: Plugins defined with `dockerfile:` are built into the local Docker engine and referenced by local image ID, which non-Docker backends cannot access. On a host without Docker, a `dockerfile:` plugin fails with an error stating that Docker is required and naming the plugin; `image:` plugins continue to work using other available sandbox backends.

Example `.ap/plugins.yaml`:
```yaml
plugins:
  astro:
    image: ghcr.io/gke-labs/astro-builder@sha256:4b22262d5395690dfa3...
    network: false
  local-tool:
    dockerfile: images/local-builder/Dockerfile
    network: false
```

- `plugins`: Map of plugin names to declarations.
- `image`: Remote container image pinned immutably by digest (`<repo>@sha256:...`). Either `image` or `dockerfile` must be specified, but not both.
- `dockerfile`: Path relative to the repository root (e.g. `images/local-builder/Dockerfile`) or a Git URL (e.g. `https://github.com/gke-labs/gke-labs-infra/blob/<commit>/ap/images/ap-plugin-astro/Dockerfile`) to a Dockerfile built automatically before discovery using that repository's root as the build context. When a Git URL is specified, the repository is fetched at the resolved commit into the cache (under `AP_CACHE_DIR`) and built using that repository's root as context, allowing plugins whose images are built from their own repository's sources to be used from anywhere else. When pinned to a full commit SHA, `ap` resolves the URL offline without contacting the remote once the image is built; when using a moving reference (such as a branch or tag), `ap` contacts the remote on discovery to verify the reference has not moved. Plugin images built from a Dockerfile follow the same builder settings as other images (local Docker buildx or remote BuildKit via `BUILDKIT_HOST` or `--buildkit-host`). Either `image` or `dockerfile` must be specified, but not both.
- `network`: Boolean flag (default `false`). When `true`, authorizes network access for the plugin if the plugin also declares that it requires network access.
- `enabled`: Optional boolean flag. When `false`, disables the plugin (useful in subtree `.ap/plugins.yaml` overrides).

Plugins declare their activation patterns as repository-relative paths (e.g. `**/astro.config.*`, `docs/astro.config.*`; a bare name matches only at the top level).

#### Astro Plugin

The Astro plugin builds static documentation sites (including Astro with Starlight) in an isolated container without requiring Node.js on the host. It activates automatically in each directory containing an Astro config matching `**/astro.config.*` and runs during `ap build`, outputting the built site to `dist/` under that directory.

Example `.ap/plugins.yaml`:
```yaml
plugins:
  astro:
    dockerfile: https://github.com/gke-labs/gke-labs-infra/blob/<commit>/ap/images/ap-plugin-astro/Dockerfile
    network: false
```

When declaring the Astro plugin by Git URL, `ap` fetches the repository at the specified commit into `AP_CACHE_DIR` and builds the plugin image with the root of that repository as the build context. This provides `go.mod`, `ap/`, and the plugin's npm dependencies (`package.json`, `package-lock.json`) in the build context so the image builds successfully from any repository.

**Supplying and Pinning Dependencies**: The builder image defines its Node.js environment and pins npm dependencies using `package.json` and `package-lock.json` alongside `ap/images/ap-plugin-astro/Dockerfile`. When built via `dockerfile:`, `ap` runs `npm ci` during image build using the repository root as context, ensuring that dependencies are hermetically locked to the repository commit. To supply custom integrations or pin updated dependency versions, modify the `package.json` and `package-lock.json` files alongside the Dockerfile. The container executes with `--network none`, ensuring that builds are strictly offline and hermetic.

## Usage

Run `go run ap/main.go` or build the binary.

```bash
ap <command>
```

### Task Output

Every leaf task runs with buffered stdout/stderr. At default verbosity, `ap` prints a single status line per task (`=== <name> (ok|FAILED, 1.2s)`) and prints captured output only for tasks that failed. For tasks that passed, warning-level log lines are printed after the per-task status line while ordinary output remains hidden. Use `-v=1` (or higher) to also print the output of successful tasks.

The `ap e2e` command prints leaf task output at default verbosity, ensuring test results (such as `--- PASS` lines) remain visible in job logs.

When `ARTIFACTS` is set (for example, in CI workflows where it points to `/tmp/artifacts`), every leaf task writes its buffered output under `$ARTIFACTS/tasks/` (with subdirectories reflecting the task tree, e.g. `$ARTIFACTS/tasks/lint-repo/go-lints/go-lint-autodeploy/go-vet.log`, and numeric suffixes resolving collisions) regardless of whether the task passed or failed. Task names are free-form human-readable strings, and the first line of each log file records the task's human name. This preserves complete task execution logs for post-run analysis and artifact downloads.

### Task Logging

Tasks should log via `scoped.Log(ctx)`, which returns a `scoped.Logger` writing into that leaf task's output buffer (command-level lines use the same form). Log messages should be constant strings with structured key/value pairs (`log.Info("msg", "key", "val")`, `log.Warn("msg", ...)`, `log.Error("msg", "error", err)`). The seven named levels and their `-v` values are: `Error`, `Warn`, and `Info` are always shown (`-v=0`), `Debug` shows at `-v=1`, `Fine` at `-v=2`, `Finer` at `-v=3`, and `Finest` at `-v=4`. Because task log lines and command outputs share the same buffer, log lines stay synchronized with command output and follow the unified task output policy.

For built images, `ap` logs a summary line at default verbosity: `built image image=<image> builder=<builder> duration=<duration>` (with `sent=<bytes>` when built with `buildctl`). The builder is `docker` or the buildkit host (`k8s://namespace/service`). For `buildctl`, `bytes` is the build-context transfer total sent from the workstation, parsed from its progress stream. For Docker, the `sent` key is omitted because Docker buildx does not expose pushed-layer transfer totals.

Commands:
- `init`: Initialize `.ap/` configuration for a repository
- `ls`: List discovered tasks under the selected subtree
- `test`: Run tests (supports `--test.race` to run unit tests under the Go data race detector)
- `e2e`: Run e2e tests
- `lint`: Run linting tasks: go vet, govulncheck, go mod tidy -diff, and the ap analyzers (`unused` [error], `testcontext` [warn], `replaceEmptyInterfaceWithAny` [warn], `testonlyimports` [error], `droppederrors` [error], `closewriter` [error], `deferinloop` [error])
- `build`: Build artifacts. Positional targets can be subtree directories (building all images and build tasks beneath them) or specific image directories (`<parent>/images/<name>` containing a `Dockerfile`, building exactly that image with the repository root as the build context). Multiple targets build the union of specified images and subtrees.
- `deploy`: Deploy artifacts
- `generate`: Run generation tasks (runs legacy `dev/tasks/generate-*` scripts, `go generate ./...` in modules that contain `//go:generate` directives, container plugin generate tasks, and built-in presubmit/workflow generators; supports `--verify` to prove generators restore perturbed outputs and verify plugin generate tasks against the checkout, `--audit-generated` to find generated files lacking headers, and `--strict`)
- `format`: Run formatting tasks
- `bash`: Run a bash script from the repository root with managed tools on PATH (use as `#!/usr/bin/env -S ap bash`, requires coreutils 8.30+)
- `versionbump`: Bump project versions (e.g. Go)
- `serve`: Start the sandbox server
- `alpha`: Experimental commands (e.g. `alpha sandbox`)
- `version`: Print version information
- `tool buildctl`: Run buildctl with pinned binary
- `tool docker`: Run docker with pinned binary and buildx plugin
- `tool docker-buildx`: Run docker-buildx with pinned binary
- `tool helm`: Run helm with pinned binary
- `tool kind`: Run kind with pinned binary
- `tool kubectl`: Run kubectl with pinned binary
- `tool kustomize`: Run kustomize with pinned binary
- `tool protoc`: Run protoc with pinned binary and language plugins
- `tool go`: Run go with pinned toolchain (GOROOT set to install directory, GOTOOLCHAIN=local)
- `tool install`: Install symlinks for managed tools pointing to `ap` (default: `${REPO_ROOT}/.build/bin` in a repository, or `AP_CACHE_DIR/bin`)
- `tool update-versions`: Update baked-in tool release versions and checksums in versions.yaml (supports `--protoc`, `--docker`, `--buildx`, `--buildctl`, `--helm`, `--kind`, `--kubectl`, `--kustomize`, `--go`)
