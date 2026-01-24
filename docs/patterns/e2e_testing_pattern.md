# E2E Testing Pattern

This document describes patterns for writing robust and developer-friendly End-to-End (E2E) tests.

## Overview

E2E tests often involve spinning up real infrastructure (like Kind clusters), which can be slow and resource-intensive. To maintain a fast inner development loop and ensure reliable CI execution, we follow specific patterns for execution control, resource management, and file system navigation.

## Key Principles

1.  **Skip by Default**: E2E tests should not run during standard unit test execution (`go test ./...`). They must be explicitly enabled via an environment variable (typically `E2E=1`).
2.  **Robust Cleanup**: Use `t.Cleanup` (or context-based cleanup) to ensure resources are torn down even if the test fails or panics. Avoid manual `defer` blocks if they can be bypassed by `os.Exit` or if `t.Cleanup` offers better integration with the test harness.
3.  **Repo-Relative Paths**: Avoid brittle relative paths like `../../testdata`. Instead, dynamically resolve the repository root by walking up the directory tree until `.git` is found.

## Example

### `tests/e2e/e2e_test.go`

```go
package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

func TestE2EScenario(t *testing.T) {
	// 1. Skip if E2E is not set
	if os.Getenv("E2E") == "" {
		t.Skip("Skipping E2E tests. Set E2E=1 to run.")
	}

	// 2. Resolve Repo Root for file access
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("Failed to find repo root: %v", err)
	}
	
	// Example: Loading a manifest relative to root
	manifestPath := filepath.Join(repoRoot, "deploy/manifests/app.yaml")
	t.Logf("Using manifest at: %s", manifestPath)

	// 3. Setup Infrastructure (e.g., Kind cluster)
	harness := NewHarness(t)
	
	// 4. Register Cleanup
	// Harness setup should ideally register its own cleanup using t.Cleanup internally,
	// but if creating resources manually, register them here.
	t.Cleanup(func() {
		// Teardown logic
		harness.Teardown()
	})

	// Run test logic...
	harness.Run(t)
}

// findRepoRoot traverses upwards to find the .git directory
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
```

## Benefits

*   **Fast Default Tests**: Developers can run `go test ./...` without waiting for heavy E2E tests.
*   **Reliability**: `t.Cleanup` ensures resources aren't leaked on test failures, keeping the dev environment clean.
*   **Portability**: Resolving paths relative to the git root means tests run correctly regardless of where `go test` is invoked (e.g., from the root or the package directory).
