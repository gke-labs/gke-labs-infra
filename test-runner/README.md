# test-runner

`test-runner` runs GitHub Actions jobs on self-hosted runners that live inside
[agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) Sandboxes.

It is deliberately small: a controller keeps a pool of ephemeral runners
registered with GitHub, each one a `Sandbox`, and replaces them as they are
used up. Jobs are ordinary GitHub Actions workflows that target the pool with
`runs-on`. A test format of our own can come later; for now GitHub Actions is
the test format.

## How it works

```
+---------------------------+      +--------------------------------------------+
| namespace: test-runner    |      | namespace: test-runner-sandboxes           |
|                           |      |                                            |
|  test-runner-controller   | ---> |  Sandbox test-runner-ab12cd34              |
|   - registers JIT runners |      |    pod: ghcr.io/actions/actions-runner     |
|     with GitHub           |      |    automountServiceAccountToken: false     |
|   - creates Sandboxes     |      |    env ACTIONS_RUNNER_INPUT_JITCONFIG      |
|   - replaces finished     |      |      <- Secret test-runner-ab12cd34        |
|     ones                  |      |  Sandbox test-runner-ef56gh78 ...          |
+---------------------------+      +--------------------------------------------+
```

For every runner the controller:

1. Asks GitHub for a [just-in-time runner configuration](https://docs.github.com/en/rest/actions/self-hosted-runners#create-configuration-for-a-just-in-time-runner).
   JIT runners are single-use: GitHub removes them after one job, and the
   configuration only works for that runner.
2. Stores the configuration in a Secret in `test-runner-sandboxes`.
3. Creates a `Sandbox` (`agents.x-k8s.io/v1beta1`) whose pod runs the official
   `actions-runner` image with the Secret injected as
   `ACTIONS_RUNNER_INPUT_JITCONFIG`. The Secret is owned by the Sandbox so it is
   garbage collected with it.

When the job finishes the runner process exits, the Sandbox reports a
`Finished` condition, and the controller deletes it and registers a fresh one,
so there are always `--replicas` idle runners waiting. Every Sandbox also gets
a `shutdownTime` (`--max-runner-lifetime`, default 6h) with
`shutdownPolicy: Delete`, so agent-sandbox tears down stuck runners even if the
controller is not running. A periodic sweep removes GitHub runner
registrations that have no Sandbox behind them.

## Isolation

The runner pods are untrusted workloads, so:

- `automountServiceAccountToken: false` on every runner pod; they have no
  Kubernetes credentials at all, and the only secret they see is their own
  single-use runner configuration.
- The controller holds only a namespaced `Role` in `test-runner-sandboxes`
  (sandboxes and secrets). It has no access to its own namespace or the rest
  of the cluster.
- `test-runner-sandboxes` enforces the `restricted` Pod Security Standard. Pods
  run as the image's non-root `runner` user, drop all capabilities, use the
  `RuntimeDefault` seccomp profile and cannot escalate privileges.
- A `NetworkPolicy` denies all ingress and limits egress to DNS plus the public
  internet; private ranges and the cloud metadata server are blocked. This
  needs a CNI that enforces NetworkPolicy (GKE Dataplane V2, Calico, ...).
- Isolation is plain containers (cgroups/namespaces) for now. `--runtime-class`
  passes a `runtimeClassName` through to the pod when a stronger runtime
  (gVisor, Kata) is available; see "Next steps".

## Deploying

Prerequisites:

```bash
# agent-sandbox
kubectl apply -f https://github.com/kubernetes-sigs/agent-sandbox/releases/download/v1.0.6/sandbox.yaml

# a GitHub token that can manage self-hosted runners. For a repository this is
# a fine-grained PAT with "Administration: read and write" on that repository,
# or a GitHub App with the same permission.
kubectl create namespace test-runner
kubectl -n test-runner create secret generic github-token --from-literal=token="$GITHUB_TOKEN"
```

Then, from the repository root, build and deploy with `ap`. Without a local
Docker daemon, point `ap` at an in-cluster buildkit (the one from
`autodeploy/k8s/docker-buildkit.yaml` works):

```bash
BUILDKIT_HOST=k8s://autodeploy-system/buildkit ap deploy //test-runner
```

Edit the `--github-repo` argument in `k8s/manifest.yaml` (or switch it to
`--github-org`) to point at the repository or organization that should get the
runners.

Workflows select the pool with its runner label, which defaults to the pool
name. Runners always also carry `self-hosted` (GitHub does not add it for
JIT runners automatically):

```yaml
jobs:
  test:
    runs-on: [self-hosted, test-runner]
    steps:
      - uses: actions/checkout@v4
      - run: go test ./...
```

## Running locally

The controller can run outside the cluster against a kubeconfig:

```bash
cd test-runner
GITHUB_TOKEN=... go run ./cmd/test-runner-controller \
  --github-repo=gke-labs/gke-labs-infra \
  --replicas=1
```

Useful flags (see `--help` for all):

| Flag | Default | Purpose |
| --- | --- | --- |
| `--replicas` | `1` | idle runners to keep waiting |
| `--pool` | `test-runner` | pool name; prefixes Sandbox and runner names |
| `--runner-labels` | pool name | GitHub labels for `runs-on`, plus `self-hosted` |
| `--runner-image` | `ghcr.io/actions/actions-runner:<version>` | runner image |
| `--runtime-class` | unset | `runtimeClassName` for runner pods |
| `--max-runner-lifetime` | `6h` | Sandbox `shutdownTime` |
| `--runner-cpu-request`, `--runner-memory-request`, `--runner-memory-limit` | `1`, `2Gi`, `4Gi` | runner resources |
| `--runner-work-size-limit` | `20Gi` | emptyDir limit for `_work` |

## Layout

- `cmd/test-runner-controller` - the controller binary
- `pkg/runnerpool` - the reconcile loop and Sandbox construction
- `pkg/github` - the small slice of the GitHub runners API that is needed
- `images/test-runner-controller` - controller image
- `k8s/manifest.yaml` - namespaces, RBAC, NetworkPolicy and Deployment

## Next steps

- **Stronger isolation.** Evaluate `runtimeClassName` options (gVisor on GKE
  Sandbox, Kata) and, if the ergonomics are better, running a microVM inside the
  Sandbox. The latter would also let us boot GitHub's own `ubuntu-latest`
  runner images instead of the bare `actions-runner` container, which has no
  Docker and few preinstalled tools.
- **Scale on demand.** Today the pool is a fixed number of idle runners. A
  `workflow_job` webhook, or polling queued jobs, could scale to zero.
- **GitHub App auth** instead of a long-lived token, with automatic rotation.
- **Our own test format**, with the GitHub runner as one execution backend.
