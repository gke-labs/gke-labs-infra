package automation

import (
	"net/http"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v81/github"
	"k8s.io/klog/v2"
)

type WebhookHandler struct {
	AppsTransport *ghinstallation.AppsTransport
	WebhookSecret []byte
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

	ctx := r.Context()

	switch event := event.(type) {
	case *github.PullRequestReviewEvent:
		h.handlePullRequestReview(ctx, event)
	case *github.CheckRunEvent:
		h.handleCheckRun(ctx, event)
	case *github.CheckSuiteEvent:
		h.handleCheckSuite(ctx, event)
	case *github.StatusEvent:
		h.handleStatus(ctx, event)
	case *github.PullRequestEvent:
		// Also useful to check when PR is opened or synced (new commits)
		h.handlePullRequest(ctx, event)
	default:
		// Ignore other events
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func (h *WebhookHandler) getClient(installationID int64) (*github.Client, error) {
	// We use the AppsTransport to create a new transport for the specific installation
	// ghinstallation.NewFromAppsTransport handles the token refresh logic
	itr := ghinstallation.NewFromAppsTransport(h.AppsTransport, installationID)
	return github.NewClient(&http.Client{Transport: itr}), nil
}
