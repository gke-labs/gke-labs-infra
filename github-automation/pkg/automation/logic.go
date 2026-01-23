// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package automation

import (
	"context"

	"github.com/google/go-github/v81/github"
	"k8s.io/klog/v2"
)

func (h *WebhookHandler) handlePullRequestReview(ctx context.Context, event *github.PullRequestReviewEvent) {
	if event.GetReview().GetState() != "approved" {
		return
	}
	h.processPR(ctx, event.GetRepo(), event.GetPullRequest(), event.GetInstallation().GetID())
}

func (h *WebhookHandler) handleCheckRun(ctx context.Context, event *github.CheckRunEvent) {
	if event.GetCheckRun().GetStatus() != "completed" {
		return
	}

	prs := event.GetCheckRun().PullRequests
	if len(prs) == 0 {
		// Fallback: look up PRs by SHA
		client, err := h.getClient(event.GetInstallation().GetID())
		if err != nil {
			klog.Errorf("Failed to create client: %v", err)
			return
		}
		foundPrs, _, err := client.PullRequests.ListPullRequestsWithCommit(ctx, event.GetRepo().GetOwner().GetLogin(), event.GetRepo().GetName(), event.GetCheckRun().GetHeadSHA(), nil)
		if err != nil {
			klog.Errorf("Failed to list PRs for commit %s: %v", event.GetCheckRun().GetHeadSHA(), err)
			return
		}
		prs = foundPrs
	}

	for _, pr := range prs {
		h.fetchAndProcessPR(ctx, event.GetRepo(), pr.GetNumber(), event.GetInstallation().GetID())
	}
}

func (h *WebhookHandler) handleCheckSuite(ctx context.Context, event *github.CheckSuiteEvent) {
	if event.GetCheckSuite().GetStatus() != "completed" {
		return
	}

	prs := event.GetCheckSuite().PullRequests
	if len(prs) == 0 {
		// Fallback: look up PRs by SHA
		client, err := h.getClient(event.GetInstallation().GetID())
		if err != nil {
			klog.Errorf("Failed to create client: %v", err)
			return
		}
		foundPrs, _, err := client.PullRequests.ListPullRequestsWithCommit(ctx, event.GetRepo().GetOwner().GetLogin(), event.GetRepo().GetName(), event.GetCheckSuite().GetHeadSHA(), nil)
		if err != nil {
			klog.Errorf("Failed to list PRs for commit %s: %v", event.GetCheckSuite().GetHeadSHA(), err)
			return
		}
		prs = foundPrs
	}

	for _, pr := range prs {
		h.fetchAndProcessPR(ctx, event.GetRepo(), pr.GetNumber(), event.GetInstallation().GetID())
	}
}

func (h *WebhookHandler) handleStatus(ctx context.Context, event *github.StatusEvent) {
	if event.GetState() != "success" {
		return
	}

	// Status event gives us a Commit SHA. We need to find open PRs for this SHA.
	client, err := h.getClient(event.GetInstallation().GetID())
	if err != nil {
		klog.Errorf("Failed to create client: %v", err)
		return
	}

	prs, _, err := client.PullRequests.ListPullRequestsWithCommit(ctx, event.GetRepo().GetOwner().GetLogin(), event.GetRepo().GetName(), event.GetSHA(), nil)
	if err != nil {
		klog.Errorf("Failed to list PRs for commit %s: %v", event.GetSHA(), err)
		return
	}

	for _, pr := range prs {
		h.processPR(ctx, event.GetRepo(), pr, event.GetInstallation().GetID())
	}
}

func (h *WebhookHandler) handlePullRequest(ctx context.Context, event *github.PullRequestEvent) {
	action := event.GetAction()
	if action == "opened" || action == "reopened" || action == "synchronize" || action == "ready_for_review" {
		h.processPR(ctx, event.GetRepo(), event.GetPullRequest(), event.GetInstallation().GetID())
	}
}

func (h *WebhookHandler) fetchAndProcessPR(ctx context.Context, repo *github.Repository, prNumber int, installationID int64) {
	client, err := h.getClient(installationID)
	if err != nil {
		klog.Errorf("Failed to create client: %v", err)
		return
	}

	pr, _, err := client.PullRequests.Get(ctx, repo.GetOwner().GetLogin(), repo.GetName(), prNumber)
	if err != nil {
		klog.Errorf("Failed to get PR %d: %v", prNumber, err)
		return
	}

	h.processPR(ctx, repo, pr, installationID)
}

func (h *WebhookHandler) processPR(ctx context.Context, repo *github.Repository, pr *github.PullRequest, installationID int64) {
	// 1. Check if PR is mergeable state generally (and not draft)
	if pr.GetDraft() {
		klog.Infof("PR %d is a draft, skipping", pr.GetNumber())
		return
	}

	if pr.GetState() != "open" {
		klog.Infof("PR %d is not open, skipping", pr.GetNumber())
		return
	}

	client, err := h.getClient(installationID)
	if err != nil {
		klog.Errorf("Failed to create client: %v", err)
		return
	}

	// 2. Check Approvals and CI
	// We need to fetch branch protection rules to know what is required
	// Note: GetBranchProtection requires admin/write access usually.

	// If the user hasn't set up branch protection, our logic might default to allowing,
	// or we can just rely on `pr.MergeableState`.
	// The prompt explicitly asks to verify approvals and CI.

	owner := repo.GetOwner().GetLogin()
	repoName := repo.GetName()
	baseRef := pr.GetBase().GetRef()

	protection, _, err := client.Repositories.GetBranchProtection(ctx, owner, repoName, baseRef)
	if err != nil {
		// If 404, no protection.
		if respErr, ok := err.(*github.ErrorResponse); ok && respErr.Response.StatusCode == 404 {
			klog.Infof("No branch protection found for %s. Relying on default mergeability.", baseRef)
			protection = nil
		} else {
			klog.Errorf("Failed to get branch protection for %s: %v", baseRef, err)
			return
		}
	}

	// Verify Approvals
	if protection != nil && protection.RequiredPullRequestReviews != nil {
		requiredCount := protection.RequiredPullRequestReviews.RequiredApprovingReviewCount
		if requiredCount > 0 {
			reviews, _, err := client.PullRequests.ListReviews(ctx, owner, repoName, pr.GetNumber(), nil)
			if err != nil {
				klog.Errorf("Failed to list reviews: %v", err)
				return
			}

			approvedCount := 0
			approvers := make(map[string]bool)
			for _, review := range reviews {
				if review.GetState() == "APPROVED" {
					approvers[review.GetUser().GetLogin()] = true
				}
			}
			approvedCount = len(approvers)

			if approvedCount < requiredCount {
				klog.Infof("PR %d has %d approvals, required %d. Skipping.", pr.GetNumber(), approvedCount, requiredCount)
				return
			}
		}
	}

	// Verify CI Checks
	if protection != nil && protection.RequiredStatusChecks != nil {
		// We need to verify that all required contexts are passing
		// Fetch Combined Status for legacy statuses
		combinedStatus, _, err := client.Repositories.GetCombinedStatus(ctx, owner, repoName, pr.GetHead().GetSHA(), nil)
		if err != nil {
			klog.Errorf("Failed to get combined status: %v", err)
			return
		}

		// Fetch Check Runs for GitHub Actions
		checkRuns, _, err := client.Checks.ListCheckRunsForRef(ctx, owner, repoName, pr.GetHead().GetSHA(), nil)
		if err != nil {
			klog.Errorf("Failed to list check runs: %v", err)
			return
		}

		// Create a map of current status/check states
		currentStates := make(map[string]string) // context -> state

		for _, status := range combinedStatus.Statuses {
			currentStates[status.GetContext()] = status.GetState() // success, pending, failure, etc.
		}

		for _, run := range checkRuns.CheckRuns {
			// Map check run conclusion/status to a state
			// If status is "completed", use conclusion.
			// If not, it's "pending".
			name := run.GetName()
			if run.GetStatus() == "completed" {
				currentStates[name] = run.GetConclusion() // success, failure, neutral, etc.
			} else {
				currentStates[name] = "pending"
			}
		}

		if protection.RequiredStatusChecks.Contexts != nil {
			for _, requiredContext := range *protection.RequiredStatusChecks.Contexts {
				state, exists := currentStates[requiredContext]
				if !exists {
					klog.Infof("PR %d missing required check: %s. Skipping.", pr.GetNumber(), requiredContext)
					return
				}
				if state != "success" {
					klog.Infof("PR %d required check %s is %s. Skipping.", pr.GetNumber(), requiredContext, state)
					return
				}
			}
		}
	}

	// If we got here, we are good to queue!
	klog.Infof("PR %d meets all criteria. Adding to merge queue.", pr.GetNumber())

	// Action: Add to Merge Queue
	_, _, err = client.PullRequests.Merge(ctx, owner, repoName, pr.GetNumber(), "Automated merge request", nil)
	if err != nil {
		klog.Errorf("Failed to add PR %d to queue: %v", pr.GetNumber(), err)
	} else {
		klog.Infof("Successfully added PR %d to merge queue (or merged).", pr.GetNumber())
	}
}
