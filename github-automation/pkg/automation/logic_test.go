package automation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/go-github/v81/github"
)

func TestProcessPR(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	client := github.NewClient(nil)
	url, _ := url.Parse(server.URL + "/")
	client.BaseURL = url
	client.UploadURL = url

	handler := &WebhookHandler{
		ClientCreator: func(installationID int64) (*github.Client, error) {
			return client, nil
		},
	}

	repo := &github.Repository{
		Owner: &github.User{Login: github.String("owner")},
		Name:  github.String("repo"),
	}
	pr := &github.PullRequest{
		Number: github.Int(1),
		State:  github.String("open"),
		Draft:  github.Bool(false),
		Base: &github.PullRequestBranch{
			Ref: github.String("main"),
		},
		Head: &github.PullRequestBranch{
			SHA: github.String("headsha"),
		},
	}

	// Mock Branch Protection
	mux.HandleFunc("/repos/owner/repo/branches/main/protection", func(w http.ResponseWriter, r *http.Request) {
		protection := &github.Protection{
			RequiredPullRequestReviews: &github.PullRequestReviewsEnforcement{
				RequiredApprovingReviewCount: 1,
			},
			RequiredStatusChecks: &github.RequiredStatusChecks{
				Contexts: &[]string{"ci/test"},
			},
		}
		json.NewEncoder(w).Encode(protection)
	})

	// Mock Reviews
	mux.HandleFunc("/repos/owner/repo/pulls/1/reviews", func(w http.ResponseWriter, r *http.Request) {
		reviews := []*github.PullRequestReview{
			{State: github.String("APPROVED"), User: &github.User{Login: github.String("approver")}},
		}
		json.NewEncoder(w).Encode(reviews)
	})

	// Mock Combined Status
	mux.HandleFunc("/repos/owner/repo/commits/headsha/status", func(w http.ResponseWriter, r *http.Request) {
		status := &github.CombinedStatus{
			Statuses: []*github.RepoStatus{
				{Context: github.String("ci/test"), State: github.String("success")},
			},
		}
		json.NewEncoder(w).Encode(status)
	})

	// Mock Check Runs (return empty for simplicity, relying on statuses)
	mux.HandleFunc("/repos/owner/repo/commits/headsha/check-runs", func(w http.ResponseWriter, r *http.Request) {
		runs := &github.ListCheckRunsResults{
			CheckRuns: []*github.CheckRun{},
		}
		json.NewEncoder(w).Encode(runs)
	})

	// Mock Merge
	merged := false
	mux.HandleFunc("/repos/owner/repo/pulls/1/merge", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			t.Errorf("Expected PUT request, got %s", r.Method)
		}
		merged = true
		w.WriteHeader(http.StatusOK)
	})

	handler.processPR(context.Background(), repo, pr, 123)

	if !merged {
		t.Error("Expected PR to be merged/queued, but it wasn't")
	}
}

func TestProcessPR_SkipDraft(t *testing.T) {
	handler := &WebhookHandler{}
	pr := &github.PullRequest{
		Number: github.Int(1),
		Draft:  github.Bool(true),
	}
	// Should return early without calling client
	handler.processPR(context.Background(), nil, pr, 123)
}
