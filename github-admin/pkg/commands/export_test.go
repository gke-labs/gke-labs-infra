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
	"reflect"
	"testing"

	"github.com/gke-labs/gke-labs-infra/github-admin/pkg/config"
	"github.com/google/go-github/v81/github"
)

func TestMapBranchProtection(t *testing.T) {
	tests := []struct {
		name string
		bp   *github.Protection
		want *config.BranchProtection
	}{
		{
			name: "Basic protection",
			bp: &github.Protection{
				EnforceAdmins:        &github.AdminEnforcement{Enabled: true},
				RequireLinearHistory: &github.RequireLinearHistory{Enabled: true},
				AllowForcePushes:     &github.AllowForcePushes{Enabled: false},
				AllowDeletions:       &github.AllowDeletions{Enabled: false},
			},
			want: &config.BranchProtection{
				EnforceAdmins:        true,
				RequireLinearHistory: true,
				AllowForcePushes:     false,
				AllowDeletions:       false,
			},
		},
		{
			name: "With status checks and reviews",
			bp: &github.Protection{
				EnforceAdmins:        &github.AdminEnforcement{Enabled: false},
				RequireLinearHistory: &github.RequireLinearHistory{Enabled: false},
				AllowForcePushes:     &github.AllowForcePushes{Enabled: true},
				AllowDeletions:       &github.AllowDeletions{Enabled: true},
				RequiredStatusChecks: &github.RequiredStatusChecks{
					Strict:   true,
					Contexts: &[]string{"ci/test", "ci/lint"},
				},
				RequiredPullRequestReviews: &github.PullRequestReviewsEnforcement{
					DismissStaleReviews:          true,
					RequireCodeOwnerReviews:      true,
					RequiredApprovingReviewCount: 2,
				},
			},
			want: &config.BranchProtection{
				EnforceAdmins:        false,
				RequireLinearHistory: false,
				AllowForcePushes:     true,
				AllowDeletions:       true,
				RequiredStatusChecks: &config.RequiredStatusChecks{
					Strict:   true,
					Contexts: []string{"ci/test", "ci/lint"},
				},
				RequiredPullRequestReviews: &config.RequiredPullRequestReviews{
					DismissStaleReviews:          true,
					RequireCodeOwnerReviews:      true,
					RequiredApprovingReviewCount: 2,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapBranchProtection(tt.bp)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("mapBranchProtection() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMapRuleset(t *testing.T) {
	targetBranch := github.RulesetTarget("branch")

	tests := []struct {
		name string
		rs   *github.RepositoryRuleset
		want *config.RepositoryRuleset
	}{
		{
			name: "Basic Ruleset",
			rs: &github.RepositoryRuleset{
				Name:        "default",
				Target:      &targetBranch,
				Enforcement: github.RulesetEnforcement("active"),
			},
			want: &config.RepositoryRuleset{
				Name:        "default",
				Target:      "branch",
				Enforcement: "active",
			},
		},
		{
			name: "Ruleset with Merge Queue",
			rs: &github.RepositoryRuleset{
				Name:        "merge-queue",
				Enforcement: "active",
				Rules: &github.RepositoryRulesetRules{
					MergeQueue: &github.MergeQueueRuleParameters{
						MergeMethod:       "SQUASH",
						MinEntriesToMerge: 1,
					},
				},
			},
			want: &config.RepositoryRuleset{
				Name:        "merge-queue",
				Enforcement: "active",
				Rules: &config.RulesetRules{
					MergeQueue: &config.MergeQueueRule{
						MergeMethod:       "SQUASH",
						MinEntriesToMerge: 1,
					},
				},
			},
		},
		{
			name: "Ruleset with Conditions",
			rs: &github.RepositoryRuleset{
				Name:        "main-protection",
				Enforcement: "active",
				Conditions: &github.RepositoryRulesetConditions{
					RefName: &github.RepositoryRulesetRefConditionParameters{
						Include: []string{"refs/heads/main"},
						Exclude: []string{"refs/heads/dev"},
					},
				},
			},
			want: &config.RepositoryRuleset{
				Name:        "main-protection",
				Enforcement: "active",
				Conditions: &config.RulesetConditions{
					RefName: &config.RefNameCondition{
						Include: []string{"refs/heads/main"},
						Exclude: []string{"refs/heads/dev"},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapRuleset(tt.rs)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("mapRuleset() = \n%v\n, want \n%v", got, tt.want)
			}
		})
	}
}
