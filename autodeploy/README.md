# autodeploy

`autodeploy` is a Kubernetes controller that monitors a git repository and automatically builds, tests, and deploys the code using `ap`.

## Overview

`autodeploy` is designed to run inside a Kubernetes cluster. It polls a target git repository for new commits. When a change is detected, it triggers a CI/CD pipeline by executing `ap` commands.

## Features (Planned)

*   **Git Polling**: Periodically checks for updates in a git repository.
*   **AP Integration**: Leverages `ap build`, `ap test`, and `ap deploy` for the CI/CD workflow.
*   **Pluggable Deployment Strategies**:
    *   Development: Deploy on every merge to the default branch.
    *   Staging/Production: Deploy based on tags or release branches.
*   **BuildKit Integration**: Uses a Docker BuildKit endpoint for building images without a local Docker daemon.
*   **Self-hosted Registry**: Includes or integrates with a self-hosted image registry for seamless image management.

## Getting Started

1. Deploy `autodeploy` to a cluster:
   ```bash
   ap deploy autodeploy
   ```
2. Apply the bootstrap packages (a one-time manual apply):
   ```bash
   kubectl apply -f autodeploy/packages.yaml
   ```
   These bootstrap `Package` objects live outside `k8s/` directories so that no `Package` deploys or retargets them.

## Package Namespace Scoping

A Package deploys into its own namespace: `autodeploy` runs `ap deploy -n <pkg.Namespace> <dir>` when deploying a package. Namespaced objects in the manifests are retargeted into the Package's namespace, while cluster-scoped objects (such as CRDs, ClusterRoles, and Namespaces) are applied as-is.

### Known breakages

*   **Cross-namespace Package objects**: A Package's manifests cannot include Package objects for other namespaces; `-n` retargets them into the deploying Package's namespace. Bootstrap Packages are applied by hand from a file outside any `k8s/` directory (e.g. `autodeploy/packages.yaml`).
*   **Multi-namespace components (`test-runner`)**: Components like `test-runner` want two namespaces (`test-runner` and `test-runner-sandboxes`, with a ServiceAccount in each). Because namespaced objects are retargeted into the Package's namespace, objects intended for secondary namespaces are retargeted into the primary Package namespace instead.
*   **Dangling `kind: Namespace` manifests**: Manifests that carry their own `kind: Namespace` now create namespaces that nothing uses because namespaced workloads in those manifests are retargeted into the Package's namespace.

