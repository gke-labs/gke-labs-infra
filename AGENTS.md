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

### Verifying changes

Before submitting a Pull Request, you MUST run `ap test`, `ap generate` and `ap lint` to verify that your changes follow the project's style and quality guidelines.

```bash
ap generate
ap lint
ap test
```

Always ensure `ap generate` is run before sending a PR, as it may regenerate CI configuration files.

Additionally, you should run the following commands to ensure that your changes do not introduce any regressions:

```bash
ap e2e
```

## Scope

- One idea, one issue, one PR. If a PR can be split into two ideas, split it; problems found during review that are outside the PR's idea become new issues.
- A good PR changes at most 200 lines including tests; review cost grows roughly with the square of the diff. If the one idea needs more, say so on the issue before pushing and propose the cut.
- Issues carry a line budget; an issue without one gets one before it is assigned.
- Separate mechanical changes (moves, renames, reformatting) from behaviour changes: own PR or at least own first commit.
- Stay in scope during review: fix what the review asks for; no unrequested improvements (compatibility shims, extra options, opportunistic refactors).
- "No new tests" in an issue means none; otherwise tests are proportionate to the change.

## Commit messages and PR descriptions

- The git history is the permanent record; the PR description is not. Anything a future reader needs goes in code comments or the commit message.
- Subject `area: imperative summary` (about 72 characters); body says why, not just what: symptom, root cause, why the change fixes it, alternatives considered for design changes, numbers and how they were measured for performance changes. If a reviewer had to ask, the answer belongs in the message.
- Keep messages true to the final code. When review changes the approach, amend the original commit or add `fixup!` commits and leave them in place (do not squash them); never leave a message describing code that was later removed.
- **The PR description is bookkeeping only: normally just `Fixes #N`.** Do not summarise or sell the change, do not paste commit messages, do not keep a changelog of review revisions in the description.

## Naming

- Do not abbreviate identifiers (`header` not `hdr`, `buffer` not `buf`); exceptions are conventional Go only: `i`/`j`/`k` indices, one- or two-letter receivers, `ctx`/`err`/`ok`/`wg`/`mu`/`tx`, protocol or on-disk field names that are themselves abbreviations.
- Names match signatures (`build...` returns bytes, `write...` takes a writer); names reflect side effects (`New...` only for fresh values); one name per concept.

## Error handling

- Never discard an error silently: return it wrapped with context, log it with context when best-effort, or handle a specific expected condition explicitly. `ap lint`'s `droppederrors` analyzer enforces this; new code must not grow the baseline.

## Go API rules

- No variadic parameters as a substitute for optional arguments; add the parameter and update every call site. Variadics are for genuine lists and functional options with a designed default.
- No nil guards or defaulted nil contexts: callers pass `context.Background()` or a real context; a nil receiver or nil required argument is a bug to crash on, not a case to support.
- No type aliases or compatibility wrappers to keep old names compiling; rename and update the callers so the diff shows the usage.
- When related values travel together or a signature grows past three return values, introduce a small struct.
- Prefer configuration in `.ap/*.yaml` over new flags or environment variables; `ap` reads no roots from the environment.
- No `regexp` in `ap` for parsing; write the plain parse. `github.com/google/go-cmp` is for tests only (`ap lint`'s `testonlyimports` analyzer enforces this).
- `ap` never mutates state outside `AP_CACHE_DIR` and the repository (no sysctls, no `~/.docker` or `~/.kube` writes); detect and warn instead.
- Dependency injection only through the `scoped` context accessors (`scoped.HTTPClient`, `scoped.Log`, `scoped.Now`); no injectable package globals.

## Logging

- New and touched code logs through `tc.Log()` inside tasks or `scoped.Log(ctx)` elsewhere, constant messages with key/value pairs, warnings as `log.Info(msg, "level", "warning")`. No new package-level `klog.*` calls; existing call sites migrate per package in their own issues (#696–#702).
