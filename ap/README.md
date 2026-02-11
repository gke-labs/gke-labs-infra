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

### Environment Variables

You can override the root discovery by setting the following environment variables:
- `AP_ROOT`: Explicitly sets the path to the ap root.
- `REPO_ROOT`: Explicitly sets the path to the git repository root.

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
gofmt: true
```

### ap.yaml

General configuration for `ap` itself.

Example `.ap/ap.yaml`:
```yaml
version: "v0.1.0"
```

## Usage

Run `go run ap/main.go` or build the binary.

```bash
ap <command>
```

Commands:
- `test`: Run tests
- `lint`: Run linting tasks (vet, govulncheck)
- `build`: Build artifacts
- `deploy`: Deploy artifacts
- `generate`: Run generation tasks
- `format`: Run formatting tasks
- `version`: Print version information
