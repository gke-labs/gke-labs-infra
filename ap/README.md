# ap

`ap` is a development automation tool for gke-labs projects. It handles tasks like testing, building, deploying, formatting, and generating code.

## Configuration

`ap` is configured via files in the `.ap/` directory. By default, it looks for this directory in the current directory and walks up the tree until it finds one.

### AP Roots

An "ap root" is any directory containing a `.ap/` sub-directory. `ap` supports having multiple ap roots within a single repository, which is useful for monorepos or projects with distinct components.

When you run `ap`, it identifies the closest ap root by walking up from your current working directory. All commands then operate relative to that ap root.

### Multiple Roots and CI

The `ap generate` command is aware of all ap roots in the repository. It will:
1.  Discover all directories containing a `.ap/` folder.
2.  Generate CI presubmit scripts (e.g., `ap-test`, `ap-lint`) in `dev/ci/presubmits/` relative to each ap root.
3.  If multiple roots are found, it appends a suffix to the generated script names (e.g., `ap-test-subdir`) to avoid collisions.
4.  Create a unified GitHub Actions workflow at `.github/workflows/ci-presubmits.yaml` that includes jobs for all scripts across all ap roots.

### Presubmit Artifacts

Presubmit scripts may write anything to `$ARTIFACTS` (set to `/tmp/artifacts` in generated GitHub Actions jobs), and it is uploaded as `artifacts-<job>` (or `artifacts-<job>-${{ matrix.shard }}` for sharded e2e jobs) upon job completion (even on failure). If no files are written, the upload step completes without producing warnings or errors.

### Environment Variables

You can override the root discovery or control test execution by setting the following environment variables:
- `AP_ROOT`: Explicitly sets the path to the ap root.
- `REPO_ROOT`: Explicitly sets the path to the git repository root.
- `AP_E2E_SHARD`: Shard selector in `i/n` format (e.g. `1/3`), selecting every n-th test starting at i from sorted `Test*` functions.
- `AP_E2E_RUN`: Passed through to `go test -run` regular expression for e2e tests.
- `AP_E2E_TIMEOUT`: Configurable timeout for e2e tests (defaults to `30m`).

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

Configures Go-specific tooling.

Example `.ap/go.yaml`:
```yaml
gofmt:
  enabled: true
```

### ap.yaml

General configuration for `ap` itself.

Example `.ap/ap.yaml`:
```yaml
version: "v0.1.0"
presubmits:
  skipIfOnlyChanged:
  - docs/*
  - README.md
e2e:
  shards: 3
  timeout: "15m"
```

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

`e2e.shards` configures the number of parallel matrix shards generated
in `.github/workflows/ci-presubmits.yaml` for `ap-e2e` jobs (default is 3).
Setting `e2e.shards: 1` disables matrix generation.

`e2e.timeout` configures the timeout for e2e tests (defaults to 30m).

## Usage

Run `go run ap/main.go` or build the binary.

```bash
ap <command>
```

Commands:
- `init`: Initialize `.ap/` configuration for a repository
- `test`: Run tests
- `e2e`: Run e2e tests (supports `--shard`, `--run`, `--timeout`)
- `lint`: Run linting tasks (vet, govulncheck)
- `build`: Build artifacts
- `deploy`: Deploy artifacts
- `generate`: Run generation tasks
- `format`: Run formatting tasks
- `version`: Print version information
