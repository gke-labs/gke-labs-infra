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

2.  **Prepare CI directories**:
    Create the directory where `ap` will generate CI presubmit scripts. This signals `ap` to manage CI generation.

    ```bash
    mkdir -p dev/ci/presubmits
    ```

3.  **Run generation**:
    Run `ap generate` to create the initial CI scripts and GitHub Actions workflows.

    ```bash
    go run github.com/gke-labs/gke-labs-infra/ap@latest generate
    ```

    This command will:
    *   Generate standard presubmit scripts in `dev/ci/presubmits/` (e.g., `ap-test`, `ap-verify-generate`).
    *   Generate a GitHub Actions workflow in `.github/workflows/ci-presubmits.yaml` that runs these scripts.

4.  **Commit changes**:
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
