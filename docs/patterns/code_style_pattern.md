# Code Style Pattern

This document describes the pattern for maintaining code consistency and style across repositories using automated tooling.

## Overview

To minimize "nits" in code reviews and ensure a consistent codebase, we rely on automated tooling to enforce style guidelines, formatting, and file headers. This allows reviewers to focus on logic and architecture rather than spacing or missing license headers.

## Key Principles

1.  **Automated Formatting**: We use the `ap` tool (specifically `ap fmt`) to format Go code. This extends standard `gofmt`/`goimports` behavior to include project-specific rules.
2.  **File Headers**: License headers should be managed automatically. `ap fmt` ensures that every source file has the correct copyright and license header, configured via `.ap/headers.yaml` or similar configuration.
3.  **One Standard**: We avoid debating style in PRs. If the tool accepts it, it is compliant. If the style is undesirable, we update the tool, not the individual PR.

## Usage

### Local Development

Developers should run the formatter before submitting a Pull Request.

```bash
# Run ap fmt to format code and add headers
go run github.com/gke-labs/gke-labs-infra/ap@latest fmt ./...
```
*(Note: Replace with the correct installation path or alias for `ap`)*

### Configuration

Repositories using this pattern should include an `.ap/` directory containing configuration:

*   `ap.yaml`: General configuration.
*   `headers.yaml`: License header templates.

## Benefits

*   **Consistency**: All files look like they were written by the same person.
*   **Compliance**: License headers are never forgotten.
*   **Velocity**: reduced back-and-forth on PRs regarding style.
