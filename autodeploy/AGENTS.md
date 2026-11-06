# autodeploy - AI Instructions

This document provides context and guidelines for AI agents working on the `autodeploy` component.

## Architecture

`autodeploy` is a Kubernetes controller. It should follow the standard controller pattern:
1.  **Reconciliation Loop**: Poll the git repository (or eventually respond to webhooks).
2.  **State Management**: Track the last processed commit hash for each branch/tag of interest.
3.  **Task Execution**: When a new commit is detected, spawn a task (or a Kubernetes Job) to run `ap`.

## Key Components to Implement

### 1. Git Monitor
A service that polls a git repository. It should be configurable with:
*   Repo URL
*   Polling interval
*   Branches/Tags to watch

### 2. Strategy Engine
A component that decides whether a commit should be deployed based on the configured strategy.
*   `AlwaysDeploy`: Deploy every commit on a specific branch.
*   `TagDeploy`: Deploy only when a new tag matches a pattern.

### 3. AP Runner
A component that executes `ap` commands. Since `autodeploy` runs in K8s:
*   It should probably launch a Kubernetes Job to run `ap`.
*   The Job needs access to a `buildkit` endpoint for Docker builds.
*   The Job needs credentials for the image registry.

## Development Roadmap

- [ ] Basic scaffolding and project structure.
- [ ] Git polling logic using `go-git` or similar.
- [ ] Kubernetes controller boilerplate (using `controller-runtime` or similar).
- [ ] Integration with `ap` via Job templates.
- [ ] Support for BuildKit and self-hosted registry.

## Patterns and Conventions

*   Use `k8s.io/client-go` and `sigs.k8s.io/controller-runtime` for Kubernetes interactions.
*   Follow the task-based model described in the root `AGENTS.md`.
*   Ensure high observability through logging and metrics.
