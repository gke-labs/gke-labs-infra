# Design: `ap` Plugin Contract

## 1. Overview and Principles

`ap` automates development tasks across repositories using convention over configuration. Today, `ap` implements a fixed set of built-in toolchains (Go, protoc, docker, kind). To support additional toolchains without baking foreign runtimes (e.g. Node.js for Astro documentation sites) into the host or `ap` binary, `ap` introduces an extensible **plugin** model.

The design operates within the following established constraints (decisions 2026-11-15):
- **Declarative Activation**: Plugins declare repository-relative file patterns (e.g. `**/astro.config.*`, `docs/astro.config.*`; a bare name matches only at the top level). The directory containing a match is the unit of work.
- **Orchestration in `ap`**: `ap` asks an activated plugin for its tasks and runs them. `ap` retains ownership of task ordering, output buffering, logging, retries, and verification.
- **Directed Outputs**: Plugins write results only to directories designated by `ap`; `ap` places results in the tree or validates them.
- **Hermetic by Default**: Plugins declare whether network access is required, running with no network when unneeded.
- **Explicit Configuration**: Only plugins configured in `.ap/plugins.yaml` (or equivalent `.ap/` configuration) are activated.
- **Unified Contract**: Built-in Go tooling and container plugins share the same interface contract; Go remains in-process initially.
- **Sandboxing**: Starts with standard container engines (`docker`/`podman`); moves toward the `gke-labs/clix` sandbox library.

---

## 2. Plugin Contract & Manifest (Runner-Neutral)

A plugin communicates its capabilities and tasks through a versioned JSON schema (`ap.plugin/v1alpha1`):
```json
{
  "apiVersion": "ap.plugin/v1alpha1",
  "name": "astro",
  "activation": ["docs/astro.config.*"],
  "network": false,
  "tasks": [
    {
      "name": "build",
      "verb": "build",
      "description": "Build Astro static documentation site",
      "outputs": ["dist"]
    }
  ]
}
```
- **Activation**: File match patterns evaluated against repository-relative paths (e.g. `["docs/astro.config.*"]`, `["**/go.mod"]`). A bare name (e.g. `go.mod`) matches only at the top level, `**/name` matches at any depth, and directory prefixes (e.g. `docs/astro.config.*`) match only in that directory.
- **Network Declaration & Authorization**: The manifest declares `network: false` (default) or `true`. A plugin declaring `network: true` also requires explicit repository authorization via `network: true` in `.ap/plugins.yaml`. If the repository does not consent, `ap` refuses execution with an error. Both must consent for network access to be enabled.
- **Caches**: To ensure hermeticity and simplicity in v1alpha1, persistent caches are omitted. Plugins run offline and build deterministically from input trees.
- **Tasks**: Each task declares:
  - `name`: Identifier (e.g. `build`).
  - `verb`: Target `ap` verb (`generate`, `format`, `lint`, `test`, `build`, `e2e`).
  - `description`: Text displayed in `ap ls`.
  - `outputs`: Relative destination paths in the workspace. Each entry is a path relative to the activated directory; the plugin writes that entry under `/out/<entry>`, and `ap` places it at `<target>/<entry>`. When `outputs` is `["."]`, the plugin writes directly to `/out`, and `ap` mirrors the hierarchy into `<target>`, enabling generator plugins to produce co-located files alongside source files.

**Runner-Neutral Execution Protocol**:
- Filesystem: Entire repository input tree arrives read-only at `/src`. Output directory is provided at `/out`. Temporary scratch space is at `/tmp`.
- Subcommands: `<entrypoint> describe` outputs the JSON manifest to stdout; `<entrypoint> run <task> --src /src --dir <path relative to /src> --out /out` executes the task, exiting 0 on success or non-zero on error.
- Discovery Caching: On cold runs, `ap` executes `describe` and caches the JSON manifest under `${AP_CACHE_DIR:-$HOME/.cache/ap}/plugins/describe/<digest-or-id>.json`. For pulled images, the key is the image digest (`sha256:...`). For `dockerfile:` plugins, the image is built before discovery and keyed by the built image ID. Subsequent commands (`ap ls`, `ap test`, Go runs) read the cached file with sub-millisecond overhead without starting containers. If no container runtime exists on cold discovery, `ap` fails with an explicit error naming the missing runtime.
- **Recommendation**: CLI protocol (`describe` producing JSON, `run <task>`), cached under `AP_CACHE_DIR`, with two-key network authorization.
- **Alternative Not Taken**: OCI image labels (e.g. `LABEL dev.ap.plugin.activation=...`) or static files in image layers. While labels avoid running containers on cold caches, they limit dynamic task generation and require specialized registry inspection tools.

---

## 3. File Tree Transfer & Staging

1. **Input File Tree**: The plugin receives the files in the repository according to `ap`'s existing `sourcetree.SourceTree` view of the repository root, which automatically respects `.gitignore` and `ap` ignore patterns. No separate exclude mechanism is introduced. The plugin is instructed where to operate via `--dir <relative path>` relative to `/src`.
2. **Container Runner Mechanics**:
   - `ap` packs the `SourceTree` view of the repository into a tar stream and copies it into `/src` (e.g. `docker cp - <container>:/`).
   - The container runs with `--network none` (unless network is declared and authorized), runs `run <task> --src /src --dir <relDir> --out /out`, and writes results to `/out`.
   - `ap` copies `/out` into a staging directory on the host (`docker cp <container>:/out/. <staging>`). Because `docker cp` extracts files as the invoking user, correct host file ownership is guaranteed even if the container executed as root.
   - `ap` validates staged files and synchronizes them to the activated directory target paths in the workspace. Moving to `gke-labs/clix` will replace `docker cp` calls with sandbox mounts without altering this contract.
- **Recommendation**: Tar streaming of `SourceTree` into `/src` and staging copy-out from `/out`.
- **Alternative Not Taken**: Direct bind mounts (`-v $(pwd):/src`). Read-write bind mounts allow plugins to mutate untracked files behind `ap`'s back, create root-owned files on Linux hosts, and break atomic staging.

---

## 4. Verify Mode

`ap generate --verify` confirms that generator tasks produce outputs matching the repository state.
1. Scope: Verify mode applies strictly to tasks with verb `generate` whose outputs are tracked repository files. Tasks with verb `build` (such as untracked site outputs in `dist`) are not run in verify mode.
2. Staging Comparison: The plugin task runs with output routed to an isolated host staging directory.
3. Diffing:
   - For dedicated output paths (e.g. `outputs: ["dist"]`), `ap` compares the staging directory recursively byte-for-byte against `<target>/dist`.
   - For co-located outputs (`outputs: ["."]`), `ap` compares only the files present in staging against their counterparts in the workspace tree, since the workspace directory also holds human-authored source files. A stale file left behind when a plugin ceases generating it is a known limitation of v1alpha1, addressed in the future via generated file manifests or orphan auditing.
   - If compared files differ, are missing, or unexpected new files appear, `ap` prints a unified diff and exits with a non-zero status.
4. No Perturbation Needed: Unlike in-process generators that write directly to the working tree, plugin outputs are directed to staging and never touch the working copy during verification.
- **Recommendation**: Recursive staging-to-workspace diff comparison without in-place modification or perturbation.
- **Alternative Not Taken**: In-place generation followed by `git diff --exit-code`. This fails when uncommitted changes exist in the working tree and cannot isolate plugin outputs.

---

## 5. Configuration & Image Naming

Plugins are declared in `.ap/plugins.yaml`:
```yaml
plugins:
  astro:
    image: ghcr.io/gke-labs/astro-builder@sha256:4b22...
    network: false
  local-tool:
    dockerfile: images/local-builder/Dockerfile
    network: false
```
- **Image Resolution**: Remote images are pinned immutably by digest (`image: <repo>@sha256:...`). Repository-local Dockerfiles (`dockerfile: <path>`) are built by `ap` via `images.BuildTasks` before discovery so that `describe` can run and the resulting image ID serves as the discovery cache key.
- **Unified Configuration Tree (#666)**: `.ap/plugins.yaml` is syntactic sugar for the `plugins:` key in `ap.yaml`. Subtree configurations inherit root plugins, allowing subtrees to override settings or disable plugins (`enabled: false`).
- **Recommendation**: Support both digest-pinned images and repo-local Dockerfiles under `plugins:` conforming to #666.
- **Alternative Not Taken**: Remote registry images only. This would prevent developing plugins locally or embedding project-specific builders.

---

## 6. Unified Go and Container Plugin Interface

Container plugins and built-in Go tooling implement the same Go interface in `ap/pkg/plugins`:
```go
type Plugin interface {
    Describe(ctx context.Context) (*PluginManifest, error)
    DiscoverTasks(ctx context.Context, dir string, scope *tasks.TaskContext) ([]PluginTask, error)
}

type PluginManifest struct {
    APIVersion string         `json:"apiVersion"`
    Name       string         `json:"name"`
    Activation []string       `json:"activation"`
    Network    bool           `json:"network,omitempty"`
    Tasks      []TaskMetadata `json:"tasks"`
}

type TaskMetadata struct {
    Name        string   `json:"name"`
    Verb        string   `json:"verb"`
    Description string   `json:"description"`
    Outputs     []string `json:"outputs"`
}

type PluginTask struct {
    TaskMetadata
    Task tasks.Task
}
```
- **Container Implementation**: `Describe` loads the cached manifest (running the container and caching under `AP_CACHE_DIR` on first run). `DiscoverTasks` returns `PluginTask` structs wrapping a `tasks.Task` that drives the container runner for the given `dir`.
- **In-Process Go Implementation**: `Describe` returns static Go metadata (`activation: ["**/go.mod"]`, tasks for `test`, `lint`, etc.). `DiscoverTasks` wraps `golang.TestTasks` and `golang.LintTasks` for the activated module at `dir`.
- **Relation to Tasks and Tools**: `tasks.Task` remains the core unit for output buffering and status printing. In-process Go tasks use `ap/pkg/tool` for host binary resolution (`go`, `govulncheck`), whereas container plugins encapsulate their tools entirely inside their images.
- **Recommendation**: Unified `Plugin` interface with identical metadata shapes for both Go and container implementations.
- **Alternative Not Taken**: Separate abstractions for built-in vs container tools, which would prevent moving Go out to a plugin in the future.

---

## 7. CLI Output & Multi-Plugin Conflicts

- **Task Naming & `ap ls`**: Plugin tasks follow standard hyphenated naming (`<plugin>-<task>`, e.g. `astro-build`), consistent with existing tasks like `docker-build-<image>` and `go-test-<module>`. In `ap ls`:
  ```
  docs/
    - build-tasks
      - astro-build
  ```
  Execution status lines: `=== astro-build docs (ok, 1.4s)`. Hyphenation avoids special shell/regex characters like colons.
- **Directory Conflicts**: When multiple plugins match the same directory (e.g. Go backend and Astro frontend):
  - Plugins activate concurrently; tasks for the requested verb execute in deterministic order (sorted by task name).
  - If two tasks declare colliding output paths, `ap` detects the overlap during discovery and aborts with a validation error before running.
- **Recommendation**: Hyphenated names, deterministic multi-plugin execution, and fail-fast output collisions.
- **Alternative Not Taken**: Winner-takes-all plugin priority, which breaks polyglot subtrees with co-located toolchains.

---

## 8. Costs & Security Model

- **Startup Latency**: Container creation and tar streaming incur an estimated 200–500ms latency per invocation. `ap` eliminates this overhead on warm runs by caching manifests under `AP_CACHE_DIR` and only launching containers when requested verbs target the plugin. In-process Go tasks incur zero container overhead.
- **Missing Container Engine**: If a configured plugin activates but no container engine is installed on the host, `ap` fails fast with an actionable error identifying the plugin, directory, and missing runtime.
- **Trust & Isolation**: Containers run with `--network none` by default and read-only source inputs. Digest pinning ensures container contents cannot mutate unnoticed.
- **Recommendation**: Cache discovery metadata, enforce `--network none` by default, and fail fast if a required container engine is absent.
- **Alternative Not Taken**: Silently skipping container plugins when engines are missing. On CI, silent skipping produces false-positive green builds that failed to generate or build required assets.

---

## 9. Implementation Sequence

The work is structured into incremental PRs of approximately 200 lines each, aligned with tracking issue #769:

1. **#770 Design Document** (This PR): Architecture and contract specification in `ap/docs/plugins.md`.
2. **#771 In-process Plugin Interface**: Define `Plugin` and `PluginTask` in `ap/pkg/plugins`; adapt Go discovery to implement it (~200 lines).
3. **#772 Configuration & Activation**: Parse `.ap/plugins.yaml`, discover cold manifests, cache under `AP_CACHE_DIR`, and match patterns (~200 lines).
4. **#773 Container Plugin Runner**: Implement container lifecycle, tar streaming, and `/out` staging collection (~200 lines).
5. **Container Verify Mode**: Implement staging diff comparison for `generate`-verb plugin tasks (~200 lines).
6. **#775 Astro Plugin (`ation`)**: Import `ation`'s builder image into `ap/images/ap-astro/`, activate on `astro.config.*`, and run site builds (~200 lines).
7. **#774 Clix Sandboxing Library**: Replace direct container engine calls with `gke-labs/clix` library sandboxing once available (~200 lines).

- **Recommendation**: Land the interface and config first, then container execution, staging verify, and the Astro plugin, deferring Clix.
- **Alternative Not Taken**: Bundling container execution and verify diffing into one PR exceeding reviewable size limits.
