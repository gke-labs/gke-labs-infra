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
	"net/http"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v81/github"
	"k8s.io/klog/v2"
)

type WebhookHandler struct {
	AppsTransport *ghinstallation.AppsTransport
	WebhookSecret []byte
	ClientCreator func(installationID int64) (*github.Client, error)
}

func (h *WebhookHandler) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	payload, err := github.ValidatePayload(r, h.WebhookSecret)
	if err != nil {
		klog.Errorf("Failed to validate webhook payload: %v", err)
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	event, err := github.ParseWebHook(github.WebHookType(r), payload)
	if err != nil {
		klog.Errorf("Failed to parse webhook: %v", err)
		http.Error(w, "Failed to parse webhook", http.StatusBadRequest)
		return
	}

	switch event := event.(type) {
	case *github.PullRequestReviewEvent:
		go h.handlePullRequestReview(context.Background(), event)
	case *github.CheckRunEvent:
		go h.handleCheckRun(context.Background(), event)
	case *github.CheckSuiteEvent:
		go h.handleCheckSuite(context.Background(), event)
	case *github.StatusEvent:
		go h.handleStatus(context.Background(), event)
	case *github.PullRequestEvent:
		// Also useful to check when PR is opened or synced (new commits)
		go h.handlePullRequest(context.Background(), event)
	default:
		// Ignore other events
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func (h *WebhookHandler) getClient(installationID int64) (*github.Client, error) {
	if h.ClientCreator != nil {
		return h.ClientCreator(installationID)
	}
	// We use the AppsTransport to create a new transport for the specific installation
	// ghinstallation.NewFromAppsTransport handles the token refresh logic
	itr := ghinstallation.NewFromAppsTransport(h.AppsTransport, installationID)
	return github.NewClient(&http.Client{Transport: itr}), nil
}
