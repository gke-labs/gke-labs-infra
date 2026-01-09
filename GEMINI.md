This is gke-labs-infra, a repository containing helpful development tooling for other gke-labs projects.

# Development Guidelines

## Dependencies
*   **Avoid new libraries**: Do not introduce new library dependencies unless absolutely necessary. We want to keep the dependency graph small and manageable.
*   **Prefer Standard Library**: Use the Go standard library whenever possible.
*   **Reuse Existing**: If a dependency is already in `go.mod`, try to use it instead of adding an alternative one (e.g., use `sigs.k8s.io/yaml` if available instead of adding `gopkg.in/yaml.v3` if it wasn't already there).
