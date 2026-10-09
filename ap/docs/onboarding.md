# Onboarding with ap

`ap` (autoproject) helps automate development tasks for gke-labs projects. This guide explains how to start using `ap` in a new or existing repository.

## Prerequisites

*   Go installed (1.27+ recommended)
*   A git repository

## Setup

1.  **Initialize `.ap` directory**:
    Run `ap init` to scaffold the `.ap/` configuration in your repository:

    ```bash
    go run github.com/gke-labs/gke-labs-infra/ap@latest init --copyright-holder "Your Organization"
    ```

    This command creates:
    *   `.ap/ap.yaml` configured with `version: latest`.
    *   `.ap/go.yaml` configuring Go formatting (`gofmt`) and `govet`.
    *   `.ap/headers.yaml` configuring file headers (defaults to `license: apache-2.0`, or pass `--license none` to disable).
    *   `.ap/ci.yaml` (optional, not created by `ap init`) overriding the `runs-on` of generated presubmit jobs, for example to send `ap-lint` and `ap-test` to self-hosted runners:

        ```yaml
        presubmits:
          ap-lint:
            runsOn: [self-hosted, test-runner]
          ap-test:
            runsOn: [self-hosted, test-runner]
        ```

    You can optionally pass `--generate` to scaffold and run generation in a single step:
    ```bash
    go run github.com/gke-labs/gke-labs-infra/ap@latest init --copyright-holder "Your Organization" --generate
    ```

2.  **Run generation**:
    If you did not pass `--generate` during initialization, run `ap generate` to create the initial CI scripts and GitHub Actions workflows:

    ```bash
    go run github.com/gke-labs/gke-labs-infra/ap@latest generate
    ```

    Note: `ap generate` also runs code formatting and stamps license headers across your sources (equivalent to running `ap fmt`).

    This command will:
    *   Create the `dev/ci/presubmits/` directory if it doesn't exist.
    *   Generate standard presubmit scripts (e.g., `ap-test`, `ap-verify-generate`).
    *   Generate a GitHub Actions workflow in `.github/workflows/ci-presubmits.yaml` that runs these scripts.

3.  **Commit changes**:
    Review the generated files and commit them to your repository.

    ```bash
    git add .ap dev/ci .github
    git commit -m "Initialize ap configuration and CI"
    ```

## Ongoing Usage

Whenever you update the `.ap` configuration or want to regenerate CI workflows (e.g., after upgrading `ap`), run the generate command again:

```bash
go run github.com/gke-labs/gke-labs-infra/ap@latest generate
```
