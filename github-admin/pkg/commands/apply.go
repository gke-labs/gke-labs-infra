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
	"errors"
	"fmt"
	"os"

	"github.com/gke-labs/gke-labs-infra/github-admin/pkg/config"
	"github.com/google/go-github/v81/github"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
	"sigs.k8s.io/yaml"
)

type ApplyOptions struct {
	ConfigPath  string
	GitHubToken string
	DryRun      bool
}

func (o *ApplyOptions) InitDefaults() {
	o.DryRun = true
}

func BuildApplyCommand() *cobra.Command {
	var opt ApplyOptions
	opt.InitDefaults()

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply github repo configurations from a file",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("command does not take positional arguments")
			}
			return RunApply(cmd.Context(), opt)
		},
	}
	cmd.Flags().StringVar(&opt.ConfigPath, "config", opt.ConfigPath, "Path to the config file")
	cmd.Flags().StringVar(&opt.GitHubToken, "token", opt.GitHubToken, "The github token (default from GITHUB_TOKEN env var)")
	cmd.Flags().BoolVar(&opt.DryRun, "dry-run", opt.DryRun, "If true, do not make changes")

	return cmd
}

func RunApply(ctx context.Context, opt ApplyOptions) error {
	if opt.ConfigPath == "" {
		return fmt.Errorf("--config is required")
	}
	if opt.GitHubToken == "" {
		opt.GitHubToken = os.Getenv("GITHUB_TOKEN")
	}
	if opt.GitHubToken == "" {
		return fmt.Errorf("--token or GITHUB_TOKEN env var is required")
	}

	data, err := os.ReadFile(opt.ConfigPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	var configs []config.RepositoryConfig
	if err := yaml.Unmarshal(data, &configs); err != nil {
		return fmt.Errorf("failed to unmarshal config: %w", err)
	}

	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: opt.GitHubToken},
	)
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)

	var errs []error
	for _, cfg := range configs {
		if err := applyRepo(ctx, client, cfg, opt.DryRun); err != nil {
			errs = append(errs, fmt.Errorf("error applying config to %s/%s: %w", cfg.Owner, cfg.Name, err))
		}
	}

	return errors.Join(errs...)
}

func applyRepo(ctx context.Context, client *github.Client, cfg config.RepositoryConfig, dryRun bool) error {
	fmt.Printf("Applying config to %s/%s...\n", cfg.Owner, cfg.Name)

	// Update Repo Settings
	repoReq := &github.Repository{
		Description: cfg.Description,
		Homepage:    cfg.Homepage,
		Private:     cfg.Private,
		Topics:      cfg.Topics,
	}

	if cfg.Settings != nil {
		repoReq.AllowAutoMerge = cfg.Settings.AllowAutoMerge
		repoReq.AllowSquashMerge = cfg.Settings.AllowSquashMerge
		repoReq.AllowMergeCommit = cfg.Settings.AllowMergeCommit
		repoReq.AllowRebaseMerge = cfg.Settings.AllowRebaseMerge
		repoReq.DeleteBranchOnMerge = cfg.Settings.DeleteBranchOnMerge
		repoReq.HasIssues = cfg.Settings.HasIssues
		repoReq.HasProjects = cfg.Settings.HasProjects
		repoReq.HasWiki = cfg.Settings.HasWiki
		repoReq.HasDownloads = cfg.Settings.HasDownloads
	}

	if !dryRun {
		_, _, err := client.Repositories.Edit(ctx, cfg.Owner, cfg.Name, repoReq)
		if err != nil {
			return fmt.Errorf("failed to edit repo: %w", err)
		}

		if len(cfg.Topics) > 0 {
			_, _, err := client.Repositories.ReplaceAllTopics(ctx, cfg.Owner, cfg.Name, cfg.Topics)
			if err != nil {
				return fmt.Errorf("failed to update topics: %w", err)
			}
		}
	} else {
		fmt.Printf("[DryRun] Would edit repo settings for %s\n", cfg.Name)
		if len(cfg.Topics) > 0 {
			fmt.Printf("[DryRun] Would update topics for %s: %v\n", cfg.Name, cfg.Topics)
		}
	}

	// Update Branch Protection
	for branch, bp := range cfg.BranchProtection {
		req := &github.ProtectionRequest{
			EnforceAdmins:        bp.EnforceAdmins,
			RequireLinearHistory: &bp.RequireLinearHistory,
			AllowForcePushes:     &bp.AllowForcePushes,
			AllowDeletions:       &bp.AllowDeletions,
		}

		if bp.RequiredStatusChecks != nil {
			req.RequiredStatusChecks = &github.RequiredStatusChecks{
				Strict:   bp.RequiredStatusChecks.Strict,
				Contexts: &bp.RequiredStatusChecks.Contexts,
			}
		}

		if bp.RequiredPullRequestReviews != nil {
			req.RequiredPullRequestReviews = &github.PullRequestReviewsEnforcementRequest{
				DismissStaleReviews:          bp.RequiredPullRequestReviews.DismissStaleReviews,
				RequireCodeOwnerReviews:      bp.RequiredPullRequestReviews.RequireCodeOwnerReviews,
				RequiredApprovingReviewCount: bp.RequiredPullRequestReviews.RequiredApprovingReviewCount,
			}
		}

		if !dryRun {
			_, _, err := client.Repositories.UpdateBranchProtection(ctx, cfg.Owner, cfg.Name, branch, req)
			if err != nil {
				return fmt.Errorf("failed to update branch protection for %s: %w", branch, err)
			}
		} else {
			fmt.Printf("[DryRun] Would update branch protection for %s branch %s\n", cfg.Name, branch)
		}
	}

	return nil
}
