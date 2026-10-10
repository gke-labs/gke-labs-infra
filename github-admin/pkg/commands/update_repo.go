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
	"fmt"
	"os"

	"github.com/google/go-github/v81/github"
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

	// 3. Create or update ruleset to enable Merge Queue
	rulesetName := "submit-queue"
	targetBranch := github.RulesetTarget("branch")
	rulesetReq := &github.RepositoryRuleset{
		Name:        rulesetName,
		Target:      &targetBranch,
		Enforcement: github.RulesetEnforcement("active"),
		Conditions: &github.RepositoryRulesetConditions{
			RefName: &github.RepositoryRulesetRefConditionParameters{
				Include: []string{"refs/heads/main"},
				Exclude: []string{},
			},
		},
		Rules: &github.RepositoryRulesetRules{
			MergeQueue: &github.MergeQueueRuleParameters{
				CheckResponseTimeoutMinutes:  60,
				GroupingStrategy:             github.MergeGroupingStrategyAllGreen,
				MaxEntriesToBuild:            5,
				MaxEntriesToMerge:            5,
				MergeMethod:                  github.MergeQueueMergeMethodMerge,
				MinEntriesToMerge:            1,
				MinEntriesToMergeWaitMinutes: 5,
			},
		},
	}

	existingRulesets, _, err := client.Repositories.GetAllRulesets(ctx, opt.Owner, opt.Repo, nil)
	if err != nil {
		if errResp, ok := err.(*github.ErrorResponse); ok && (errResp.Response.StatusCode == 404 || errResp.Response.StatusCode == 403) {
			fmt.Printf("Warning: Failed to list existing rulesets (rulesets may not be supported on this repository): %v\n", err)
			return nil
		}
		return fmt.Errorf("failed to list existing rulesets: %w", err)
	}

	var existingID *int64
	for _, rs := range existingRulesets {
		if rs.Name == rulesetName {
			existingID = rs.ID
			break
		}
	}

	if existingID != nil {
		_, _, err = client.Repositories.UpdateRuleset(ctx, opt.Owner, opt.Repo, *existingID, *rulesetReq)
		if err != nil {
			if errResp, ok := err.(*github.ErrorResponse); ok && errResp.Response.StatusCode == 422 {
				fmt.Printf("Warning: Failed to update merge queue ruleset (this is expected on personal forks which do not support merge queues): %v\n", err)
				return nil
			}
			return fmt.Errorf("failed to update ruleset %q: %w", rulesetName, err)
		}
		fmt.Printf("Ruleset %q updated (Merge Queue enabled).\n", rulesetName)
	} else {
		_, _, err = client.Repositories.CreateRuleset(ctx, opt.Owner, opt.Repo, *rulesetReq)
		if err != nil {
			if errResp, ok := err.(*github.ErrorResponse); ok && errResp.Response.StatusCode == 422 {
				fmt.Printf("Warning: Failed to create merge queue ruleset (this is expected on personal forks which do not support merge queues): %v\n", err)
				return nil
			}
			return fmt.Errorf("failed to create ruleset %q: %w", rulesetName, err)
		}
		fmt.Printf("Ruleset %q created (Merge Queue enabled).\n", rulesetName)
	}

	return nil
}
