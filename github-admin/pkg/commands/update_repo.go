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

package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/google/go-github/v60/github"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

type UpdateRepoOptions struct {
	Owner       string
	Repo        string
	GitHubToken string
}

func (o *UpdateRepoOptions) InitDefaults() {
}

func BuildUpdateRepoCommand() *cobra.Command {
	var opt UpdateRepoOptions
	opt.InitDefaults()

	cmd := &cobra.Command{
		Use:   "update-repo",
		Short: "Configure github repo settings (branch protection, submit queue)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("command does not take positional arguments")
			}
			return RunUpdateRepo(cmd.Context(), opt)
		},
	}
	cmd.Flags().StringVar(&opt.Owner, "owner", opt.Owner, "The github owner")
	cmd.Flags().StringVar(&opt.Repo, "repo", opt.Repo, "The github repo name")
	cmd.Flags().StringVar(&opt.GitHubToken, "token", opt.GitHubToken, "The github token (default from GITHUB_TOKEN env var)")

	return cmd
}

func RunUpdateRepo(ctx context.Context, opt UpdateRepoOptions) error {
	if opt.Owner == "" {
		return fmt.Errorf("--owner is required")
	}
	if opt.Repo == "" {
		return fmt.Errorf("--repo is required")
	}
	if opt.GitHubToken == "" {
		opt.GitHubToken = os.Getenv("GITHUB_TOKEN")
	}
	if opt.GitHubToken == "" {
		return fmt.Errorf("--token or GITHUB_TOKEN env var is required")
	}

	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: opt.GitHubToken},
	)
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)

	fmt.Printf("Updating repo %s/%s...\n", opt.Owner, opt.Repo)

	// 1. Enable Auto-Merge (prerequisite for Merge Queue)
	repoReq := &github.Repository{
		AllowAutoMerge:      github.Bool(true),
		AllowSquashMerge:    github.Bool(false),
		AllowMergeCommit:    github.Bool(true),
		AllowRebaseMerge:    github.Bool(false),
		DeleteBranchOnMerge: github.Bool(false),
	}

	_, _, err := client.Repositories.Edit(ctx, opt.Owner, opt.Repo, repoReq)
	if err != nil {
		return fmt.Errorf("failed to update repo settings: %w", err)
	}
	fmt.Println("Repo settings updated (AutoMerge enabled).")

	// 2. Branch Protection
	// We configure branch protection for 'main'
	protectionRequest := &github.ProtectionRequest{
		RequiredStatusChecks: &github.RequiredStatusChecks{
			Strict: false, // Require branches to be up to date before merging
			Contexts: &[]string{
				"ap-verify-generate",
				"ap-test",
			}, // TODO: Populate with specific checks if known, or let user configure
		},
		RequiredPullRequestReviews: &github.PullRequestReviewsEnforcementRequest{
			DismissStaleReviews:          false,
			RequireCodeOwnerReviews:      true,
			RequiredApprovingReviewCount: 1,
		},
		EnforceAdmins: false,
	}

	_, _, err = client.Repositories.UpdateBranchProtection(ctx, opt.Owner, opt.Repo, "main", protectionRequest)
	if err != nil {
		return fmt.Errorf("failed to update branch protection: %w", err)
	}
	fmt.Println("Branch protection updated for 'main'.")

	// 3. Merge Queue (via Ruleset)
	if err := ensureMergeQueue(ctx, client, opt.Owner, opt.Repo); err != nil {
		return fmt.Errorf("failed to ensure merge queue: %w", err)
	}
	fmt.Println("Merge Queue ruleset ensured.")

	return nil
}

func ensureMergeQueue(ctx context.Context, client *github.Client, owner, repo string) error {
	rulesets, _, err := client.Repositories.GetAllRulesets(ctx, owner, repo, false)
	if err != nil {
		return fmt.Errorf("failed to list rulesets: %w", err)
	}

	var existing *github.Ruleset
	for _, rs := range rulesets {
		if rs.Name == "Merge Queue" {
			existing = rs
			break
		}
	}

	// Define the merge queue rule
	params := map[string]interface{}{
		"merge_method":                   "MERGE",
		"grouping_strategy":              "HEADGREEN",
		"min_merges_to_queue":            1,
		"check_response_timeout_minutes": 60,
	}
	paramsBytes, err := json.Marshal(params)
	if err != nil {
		return err
	}
	rawParams := json.RawMessage(paramsBytes)

	target := github.String("branch")

	rules := []*github.RepositoryRule{
		{
			Type:       "merge_queue",
			Parameters: &rawParams,
		},
	}

	conditions := &github.RulesetConditions{
		RefName: &github.RulesetRefConditionParameters{
			Include: []string{"refs/heads/main"},
			Exclude: []string{},
		},
	}

	rs := &github.Ruleset{
		Name:        "Merge Queue",
		Target:      target,
		Enforcement: "active",
		Rules:       rules,
		Conditions:  conditions,
	}

	if existing != nil {
		fmt.Printf("Updating existing Merge Queue ruleset (ID: %d)...\n", *existing.ID)
		_, _, err = client.Repositories.UpdateRuleset(ctx, owner, repo, *existing.ID, rs)
		if err != nil {
			return fmt.Errorf("failed to update ruleset: %w", err)
		}
	} else {
		fmt.Printf("Creating new Merge Queue ruleset...\n")
		_, _, err = client.Repositories.CreateRuleset(ctx, owner, repo, rs)
		if err != nil {
			return fmt.Errorf("failed to create ruleset: %w", err)
		}
	}
	return nil
}
