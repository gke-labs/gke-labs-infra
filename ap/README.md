# ap

`ap` is a development automation tool for gke-labs projects. It handles tasks like testing, building, deploying, formatting, and generating code.

## Configuration

`ap` is configured via files in the `.ap/` directory at the root of the repository.

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
- `build`: Build artifacts
- `deploy`: Deploy artifacts
- `generate`: Run generation tasks
- `format`: Run formatting tasks
- `version`: Print version information
