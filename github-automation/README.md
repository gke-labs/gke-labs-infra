# GitHub Automation

This application is a GitHub App that automates various workflow tasks for gke-labs repositories.

## Features

### Merge Queue Automation
Automatically adds Pull Requests to the Merge Queue (or merges them) when they meet all criteria:
- The PR is not a Draft.
- The PR has the required number of approvals (based on branch protection).
- The PR has passed all required CI checks (based on branch protection).

It listens to the following webhooks:
- `pull_request_review`: Triggers when a review is submitted.
- `check_run` / `check_suite`: Triggers when GitHub Actions complete.
- `status`: Triggers when external CI statuses (like Prow) update.
- `pull_request`: Triggers when the PR is opened, reopened, or synchronized.

## Configuration

The application requires the following environment variables:

| Variable | Description |
|----------|-------------|
| `GITHUB_APP_ID` | The App ID of the GitHub App. |
| `GITHUB_APP_PRIVATE_KEY_PATH` | Path to the private key file (PEM) for the GitHub App. |
| `GITHUB_WEBHOOK_SECRET` | The secret used to secure the webhook endpoint. |
| `PORT` | (Optional) The port to listen on. Defaults to `8080`. |

## Deployment

1. **Create a GitHub App**:
   - Enable Webhooks and set the URL to your deployed service (e.g., `https://your-domain.com/webhook`).
   - Set the Webhook Secret.
   - Generate a Private Key.

2. **Permissions**:
   - **Pull requests**: Read & Write (to merge/queue PRs).
   - **Checks**: Read (to check CI status).
   - **Statuses**: Read (to check commit status).
   - **Metadata**: Read (default).
   - **Administration**: Read (to read branch protection rules).

3. **Subscribe to Events**:
   - `Check run`
   - `Check suite`
   - `Pull request`
   - `Pull request review`
   - `Status`

4. **Run the Service**:
   ```bash
   export GITHUB_APP_ID=12345
   export GITHUB_APP_PRIVATE_KEY_PATH=/path/to/key.pem
   export GITHUB_WEBHOOK_SECRET=your_secret
   go run ./github-automation/main.go
   ```
