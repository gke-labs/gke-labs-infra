This is gke-labs-infra, a repository containing helpful development tooling for other gke-labs projects.

# Development Guidelines

## Task-based model

The `ap` tool follows a task-based model for its operations (like `build`, `deploy`, `test`, etc.).
Each top-level command is considered an "epic" that expands into a tree of executable tasks.

### Core Principles
*   **Separation of Concerns**: Task building (planning) is strictly separated from task execution.
*   **Dry Run Support**: Every command should support a `--dry-run` flag that displays the task tree without executing it.
*   **Granularity**: Tasks should be broken down into independent, logical units (e.g., building a single image, applying a single manifest) to support partial execution and better observability.
*   **Determinism**: Tasks should be executed in a deterministic order, typically sorted by name.

### Implementing a Task
A task must implement the `Task` interface:
```go
type Task interface {
    Run(ctx context.Context, root string) error
    GetName() string
    GetChildren() []Task
}
```
Use `tasks.Group` to group related tasks together.

### Executing Tasks
Use the `tasks.Run` helper to execute tasks. It takes `RunOptions` which include `DryRun`.
When `DryRun` is true, `tasks.Run` will print the task tree instead of executing it.

### Best Practices
*   **Avoid side effects during task building**: The process of creating the task tree should be fast and side-effect free. Heavy operations should happen inside the `Run` method.
*   **Deterministic Order**: When building a `tasks.Group`, ensure the order of tasks is deterministic (e.g., by sorting by name).
*   **Granular Tasks**: Prefer many small tasks over one large task. For example, building each docker image should be its own task.
