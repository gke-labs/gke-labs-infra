package commands

import (
	"context"
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
	o.GitHubToken = os.Getenv("GITHUB_TOKEN")
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
		AllowSquashMerge:    github.Bool(true),
		AllowMergeCommit:    github.Bool(false),
		AllowRebaseMerge:    github.Bool(false),
		DeleteBranchOnMerge: github.Bool(true),
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
			Strict:   true,        // Require branches to be up to date before merging
			Contexts: &[]string{}, // TODO: Populate with specific checks if known, or let user configure
		},
		RequiredPullRequestReviews: &github.PullRequestReviewsEnforcementRequest{
			DismissStaleReviews:          true,
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

	return nil
}
