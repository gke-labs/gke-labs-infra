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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/github-admin/pkg/config"
	"github.com/google/go-github/v81/github"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
	"sigs.k8s.io/yaml"
)

type ExportOptions struct {
	Owner       string
	Repo        string
	GitHubToken string
	Output      string
}

func (o *ExportOptions) InitDefaults() {
	o.Output = "-" // stdout
}

func BuildExportCommand() *cobra.Command {
	var opt ExportOptions
	opt.InitDefaults()

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export github repo configurations",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return fmt.Errorf("command does not take positional arguments")
			}
			return RunExport(cmd.Context(), opt)
		},
	}
	cmd.Flags().StringVar(&opt.Owner, "owner", opt.Owner, "The github owner (org or user)")
	cmd.Flags().StringVar(&opt.Repo, "repo", opt.Repo, "The specific repo to export")
	cmd.Flags().StringVar(&opt.GitHubToken, "token", opt.GitHubToken, "The github token (default from GITHUB_TOKEN env var)")
	cmd.Flags().StringVar(&opt.Output, "output", opt.Output, "Output file path (default is stdout)")

	return cmd
}

func RunExport(ctx context.Context, opt ExportOptions) error {
	if opt.Owner == "" {
		return fmt.Errorf("--owner is required")
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

	type RepoRef struct {
		Owner string
		Name  string
	}
	var repoRefs []RepoRef

	if opt.Repo != "" {
		repoRefs = []RepoRef{{Owner: opt.Owner, Name: opt.Repo}}
	} else {
		// List all repositories
		repos, err := listRepositories(ctx, client, opt.Owner)
		if err != nil {
			return err
		}
		for _, repo := range repos {
			repoRefs = append(repoRefs, RepoRef{Owner: repo.GetOwner().GetLogin(), Name: repo.GetName()})
		}
	}

	// Check if we are in multi-file mode
	multiFile := strings.Contains(opt.Output, "{org}") || strings.Contains(opt.Output, "{repo}")

	var configs []config.RepositoryConfig
	var errs []error

	for _, ref := range repoRefs {
		fmt.Fprintf(os.Stderr, "Processing repo %s...\n", ref.Name)

		repo, _, err := client.Repositories.Get(ctx, ref.Owner, ref.Name)
		if err != nil {
			errs = append(errs, fmt.Errorf("error getting repo %s/%s: %w", ref.Owner, ref.Name, err))
			continue
		}

		cfg, err := exportRepo(ctx, client, repo)
		if err != nil {
			errs = append(errs, fmt.Errorf("error exporting repo %s: %w", ref.Name, err))
			continue
		}

		if multiFile {
			path := resolveOutputPath(opt.Output, cfg)
			if err := writeRepoConfig(path, cfg); err != nil {
				errs = append(errs, err)
			}
		} else {
			configs = append(configs, *cfg)
		}
	}

	if !multiFile {
		var buf bytes.Buffer
		for i, cfg := range configs {
			if i > 0 {
				buf.WriteString("---\n")
			}
			data, err := yaml.Marshal(cfg)
			if err != nil {
				errs = append(errs, fmt.Errorf("failed to marshal config: %w", err))
				return errors.Join(errs...)
			}
			buf.Write(data)
		}

		if opt.Output == "-" {
			fmt.Print(buf.String())
		} else {
			if err := os.WriteFile(opt.Output, buf.Bytes(), 0644); err != nil {
				errs = append(errs, fmt.Errorf("failed to write output file: %w", err))
			}
		}
	}

	return errors.Join(errs...)
}

func resolveOutputPath(template string, cfg *config.RepositoryConfig) string {
	path := template
	path = strings.ReplaceAll(path, "{org}", cfg.Owner)
	path = strings.ReplaceAll(path, "{repo}", cfg.Name)
	return path
}

func writeRepoConfig(path string, cfg *config.RepositoryConfig) error {
	// Create directory if needed
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config for %s: %w", cfg.Name, err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write output file %s: %w", path, err)
	}
	return nil
}

func listRepositories(ctx context.Context, client *github.Client, owner string) ([]*github.Repository, error) {
	var allRepos []*github.Repository
	opt := &github.RepositoryListByOrgOptions{
		ListOptions: github.ListOptions{PerPage: 100},
	}

	// Try listing as Org first
	for {
		repos, resp, err := client.Repositories.ListByOrg(ctx, owner, opt)
		if err != nil {
			// If not an org, try as user? Or assume org as per requirement
			// The issue says "list all the repos in an organization"
			return nil, fmt.Errorf("failed to list repos for org %s: %w", owner, err)
		}
		allRepos = append(allRepos, repos...)
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}
	return allRepos, nil
}

func exportRepo(ctx context.Context, client *github.Client, repo *github.Repository) (*config.RepositoryConfig, error) {
	cfg := &config.RepositoryConfig{
		Owner:       repo.GetOwner().GetLogin(),
		Name:        repo.GetName(),
		Description: repo.Description,
		Homepage:    repo.Homepage,
		Private:     repo.Private,
		Topics:      repo.Topics,
		Settings: &config.RepositorySettings{
			AllowAutoMerge:      repo.AllowAutoMerge,
			AllowSquashMerge:    repo.AllowSquashMerge,
			AllowMergeCommit:    repo.AllowMergeCommit,
			AllowRebaseMerge:    repo.AllowRebaseMerge,
			DeleteBranchOnMerge: repo.DeleteBranchOnMerge,
			MergeCommitTitle:    repo.MergeCommitTitle,
			MergeCommitMessage:  repo.MergeCommitMessage,
			HasIssues:           repo.HasIssues,
			HasProjects:         repo.HasProjects,
			HasWiki:             repo.HasWiki,
			HasDownloads:        repo.HasDownloads,
		},
		BranchProtection: make(map[string]*config.BranchProtection),
	}

	// Get branches to check for protection
	// We specifically care about 'main' but we can check all branches
	// Listing all branches can be expensive for large repos.
	// For now, let's just check 'main' as per current update_repo logic,
	// or maybe list branches and check which are protected.

	branches, _, err := client.Repositories.ListBranches(ctx, repo.GetOwner().GetLogin(), repo.GetName(), &github.BranchListOptions{
		Protected:   github.Bool(true),
		ListOptions: github.ListOptions{PerPage: 100},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list protected branches: %w", err)
	}

	for _, branch := range branches {
		bp, _, err := client.Repositories.GetBranchProtection(ctx, repo.GetOwner().GetLogin(), repo.GetName(), branch.GetName())
		if err != nil {
			if resp, ok := err.(*github.ErrorResponse); ok && resp.Response.StatusCode == 404 {
				// Should not happen if we listed protected branches, but good safety
				continue
			}
			return nil, fmt.Errorf("failed to get branch protection for %s: %w", branch.GetName(), err)
		}

		cfg.BranchProtection[branch.GetName()] = mapBranchProtection(bp)
	}

	// Export Rulesets
	rulesets, _, err := client.Repositories.GetAllRulesets(ctx, repo.GetOwner().GetLogin(), repo.GetName(), nil)
	if err != nil {
		if resp, ok := err.(*github.ErrorResponse); ok && resp.Response.StatusCode == 404 {
			// Rulesets might not be supported or available
		} else {
			return nil, fmt.Errorf("failed to get rulesets: %w", err)
		}
	} else {
		for _, rs := range rulesets {
			cfg.Rulesets = append(cfg.Rulesets, mapRuleset(rs))
		}
	}

	return cfg, nil
}

func mapRuleset(rs *github.RepositoryRuleset) *config.RepositoryRuleset {
	res := &config.RepositoryRuleset{
		Name:        rs.Name,
		Enforcement: string(rs.Enforcement),
	}
	if rs.Target != nil {
		res.Target = string(*rs.Target)
	}

	if rs.Conditions != nil && rs.Conditions.RefName != nil {
		res.Conditions = &config.RulesetConditions{
			RefName: &config.RefNameCondition{
				Include: rs.Conditions.RefName.Include,
				Exclude: rs.Conditions.RefName.Exclude,
			},
		}
	}

	if rs.Rules != nil {
		res.Rules = &config.RulesetRules{}
		if rs.Rules.MergeQueue != nil {
			mq := rs.Rules.MergeQueue
			res.Rules.MergeQueue = &config.MergeQueueRule{
				CheckResponseTimeoutMinutes:  mq.CheckResponseTimeoutMinutes,
				GroupingStrategy:             string(mq.GroupingStrategy),
				MaxEntriesToBuild:            mq.MaxEntriesToBuild,
				MaxEntriesToMerge:            mq.MaxEntriesToMerge,
				MergeMethod:                  string(mq.MergeMethod),
				MinEntriesToMerge:            mq.MinEntriesToMerge,
				MinEntriesToMergeWaitMinutes: mq.MinEntriesToMergeWaitMinutes,
			}
		}
	}
	return res
}

func mapBranchProtection(bp *github.Protection) *config.BranchProtection {
	res := &config.BranchProtection{
		EnforceAdmins:        bp.GetEnforceAdmins().Enabled,
		RequireLinearHistory: bp.GetRequireLinearHistory().Enabled,
		AllowForcePushes:     bp.GetAllowForcePushes().Enabled,
		AllowDeletions:       bp.GetAllowDeletions().Enabled,
	}

	if bp.RequiredStatusChecks != nil {
		var contexts []string
		if bp.RequiredStatusChecks.Contexts != nil {
			contexts = *bp.RequiredStatusChecks.Contexts
		}
		res.RequiredStatusChecks = &config.RequiredStatusChecks{
			Strict:   bp.RequiredStatusChecks.Strict,
			Contexts: contexts,
		}
	}

	if bp.RequiredPullRequestReviews != nil {
		res.RequiredPullRequestReviews = &config.RequiredPullRequestReviews{
			DismissStaleReviews:          bp.RequiredPullRequestReviews.DismissStaleReviews,
			RequireCodeOwnerReviews:      bp.RequiredPullRequestReviews.RequireCodeOwnerReviews,
			RequiredApprovingReviewCount: bp.RequiredPullRequestReviews.RequiredApprovingReviewCount,
		}
	}

	return res
}
