# Onboarding with ap

`ap` (autoproject) helps automate development tasks for gke-labs projects. This guide explains how to start using `ap` in a new or existing repository.

## Prerequisites

*   Go installed (1.21+ recommended)
*   A git repository

## Setup

1.  **Configure `.ap` directory**:
    Copy the `.ap` directory from the [gke-labs-infra repository](https://github.com/gke-labs/gke-labs-infra/tree/main/.ap) to the root of your repository.
    This directory contains configuration for `ap` tools.

    ```bash
    # Example: Copy from a local checkout or manually create the files
    mkdir .ap
    # Download headers.yaml, go.yaml, ap.yaml...
    ```

    Ensure `headers.yaml` has the correct license and copyright holder for your project.

    Ensure `.ap/ap.yaml` is configured with `version: latest`.
    (Note: The source repo uses `version: "!self"` which is for internal development only).

2.  **Run generation**:
    Run `ap generate` to create the initial CI scripts and GitHub Actions workflows.

    ```bash
    go run github.com/gke-labs/gke-labs-infra/ap@latest generate
    ```

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
