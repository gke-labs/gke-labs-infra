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

// Package github wraps the small part of the GitHub Actions API that the
// test-runner controller needs: registering just-in-time (JIT) runners and
// removing them again.
package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	gh "github.com/google/go-github/v81/github"
)

// Runners is the subset of the GitHub Actions self-hosted runner API used by
// the controller. It is an interface so that the reconcile loop can be tested
// without talking to GitHub.
type Runners interface {
	// GenerateJITConfig registers a new ephemeral runner and returns the
	// encoded configuration that the runner binary consumes.
	GenerateJITConfig(ctx context.Context, req JITRequest) (*JITRunner, error)
	// RemoveRunner deregisters a runner. Removing a runner that no longer
	// exists is not an error.
	RemoveRunner(ctx context.Context, id int64) error
	// ListRunners lists all runners registered in the scope.
	ListRunners(ctx context.Context) ([]Runner, error)
}

// JITRequest describes the runner to register.
type JITRequest struct {
	Name          string
	Labels        []string
	RunnerGroupID int64
	WorkFolder    string
}

// JITRunner is the result of registering a JIT runner.
type JITRunner struct {
	ID            int64
	EncodedConfig string
}

// Runner is a registered self-hosted runner.
type Runner struct {
	ID     int64
	Name   string
	Status string
	Busy   bool
}

// Scope identifies where runners are registered: a repository
// (owner/repo) or, when Repo is empty, an organization.
type Scope struct {
	Owner string
	Repo  string
}

// ParseScope parses "owner/repo" into a repository scope, or "org" into an
// organization scope.
func ParseScope(repo, org string) (Scope, error) {
	switch {
	case repo != "" && org != "":
		return Scope{}, fmt.Errorf("specify only one of a repository or an organization")
	case repo != "":
		owner, name, ok := strings.Cut(repo, "/")
		if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			return Scope{}, fmt.Errorf("repository %q must be in the form owner/repo", repo)
		}
		return Scope{Owner: owner, Repo: name}, nil
	case org != "":
		return Scope{Owner: org}, nil
	default:
		return Scope{}, fmt.Errorf("a repository or an organization is required")
	}
}

// String returns the scope in a human readable form.
func (s Scope) String() string {
	if s.Repo == "" {
		return s.Owner
	}
	return s.Owner + "/" + s.Repo
}

// Client implements Runners against the real GitHub API.
type Client struct {
	gh    *gh.Client
	scope Scope
}

var _ Runners = &Client{}

// NewClient creates a client authenticated with a personal access token or a
// GitHub App installation token.
func NewClient(token string, scope Scope) *Client {
	return &Client{
		gh:    gh.NewClient(nil).WithAuthToken(token),
		scope: scope,
	}
}

// GenerateJITConfig implements Runners.
func (c *Client) GenerateJITConfig(ctx context.Context, req JITRequest) (*JITRunner, error) {
	ghReq := &gh.GenerateJITConfigRequest{
		Name:          req.Name,
		RunnerGroupID: req.RunnerGroupID,
		Labels:        req.Labels,
	}
	if req.WorkFolder != "" {
		ghReq.WorkFolder = gh.Ptr(req.WorkFolder)
	}

	var (
		cfg *gh.JITRunnerConfig
		err error
	)
	if c.scope.Repo != "" {
		cfg, _, err = c.gh.Actions.GenerateRepoJITConfig(ctx, c.scope.Owner, c.scope.Repo, ghReq)
	} else {
		cfg, _, err = c.gh.Actions.GenerateOrgJITConfig(ctx, c.scope.Owner, ghReq)
	}
	if err != nil {
		return nil, fmt.Errorf("generating JIT config for runner %q in %s: %w", req.Name, c.scope, err)
	}
	if cfg.EncodedJITConfig == nil || *cfg.EncodedJITConfig == "" {
		return nil, fmt.Errorf("GitHub returned an empty JIT config for runner %q", req.Name)
	}
	out := &JITRunner{EncodedConfig: *cfg.EncodedJITConfig}
	if cfg.Runner != nil && cfg.Runner.ID != nil {
		out.ID = *cfg.Runner.ID
	}
	return out, nil
}

// RemoveRunner implements Runners.
func (c *Client) RemoveRunner(ctx context.Context, id int64) error {
	var (
		resp *gh.Response
		err  error
	)
	if c.scope.Repo != "" {
		resp, err = c.gh.Actions.RemoveRunner(ctx, c.scope.Owner, c.scope.Repo, id)
	} else {
		resp, err = c.gh.Actions.RemoveOrganizationRunner(ctx, c.scope.Owner, id)
	}
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil
		}
		return fmt.Errorf("removing runner %d from %s: %w", id, c.scope, err)
	}
	return nil
}

// ListRunners implements Runners.
func (c *Client) ListRunners(ctx context.Context) ([]Runner, error) {
	var out []Runner
	opts := &gh.ListRunnersOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	for {
		var (
			runners *gh.Runners
			resp    *gh.Response
			err     error
		)
		if c.scope.Repo != "" {
			runners, resp, err = c.gh.Actions.ListRunners(ctx, c.scope.Owner, c.scope.Repo, opts)
		} else {
			runners, resp, err = c.gh.Actions.ListOrganizationRunners(ctx, c.scope.Owner, opts)
		}
		if err != nil {
			return nil, fmt.Errorf("listing runners in %s: %w", c.scope, err)
		}
		for _, r := range runners.Runners {
			out = append(out, Runner{
				ID:     r.GetID(),
				Name:   r.GetName(),
				Status: r.GetStatus(),
				Busy:   r.GetBusy(),
			})
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}
