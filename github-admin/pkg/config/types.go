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

package config

// RepositoryConfig represents the configuration of a GitHub repository.
type RepositoryConfig struct {
	// Owner is the GitHub organization or user.
	// +optional
	Owner string `json:"owner,omitempty"`

	// Name is the name of the repository.
	Name string `json:"name"`

	// Description is the repository description.
	// +optional
	Description *string `json:"description,omitempty"`

	// Homepage is the repository homepage URL.
	// +optional
	Homepage *string `json:"homepage,omitempty"`

	// Private indicates if the repository is private.
	// +optional
	Private *bool `json:"private,omitempty"`

	// Topics is a list of topics.
	// +optional
	Topics []string `json:"topics,omitempty"`

	// Settings contains repository settings.
	// +optional
	Settings *RepositorySettings `json:"settings,omitempty"`

	// BranchProtection defines protection rules for branches.
	// The key is the branch pattern (e.g., "main").
	// +optional
	BranchProtection map[string]*BranchProtection `json:"branchProtection,omitempty"`

	// Rulesets defines the repository rulesets.
	// +optional
	Rulesets []*RepositoryRuleset `json:"rulesets,omitempty"`
}

type RepositorySettings struct {
	AllowAutoMerge      *bool `json:"allowAutoMerge,omitempty"`
	AllowSquashMerge    *bool `json:"allowSquashMerge,omitempty"`
	AllowMergeCommit    *bool `json:"allowMergeCommit,omitempty"`
	AllowRebaseMerge    *bool `json:"allowRebaseMerge,omitempty"`
	DeleteBranchOnMerge *bool `json:"deleteBranchOnMerge,omitempty"`

	MergeCommitTitle   *string `json:"mergeCommitTitle,omitempty"`
	MergeCommitMessage *string `json:"mergeCommitMessage,omitempty"`

	HasIssues    *bool `json:"hasIssues,omitempty"`
	HasProjects  *bool `json:"hasProjects,omitempty"`
	HasWiki      *bool `json:"hasWiki,omitempty"`
	HasDownloads *bool `json:"hasDownloads,omitempty"`
}

type BranchProtection struct {
	RequiredStatusChecks       *RequiredStatusChecks       `json:"requiredStatusChecks,omitempty"`
	RequiredPullRequestReviews *RequiredPullRequestReviews `json:"requiredPullRequestReviews,omitempty"`
	EnforceAdmins              bool                        `json:"enforceAdmins,omitempty"`
	RequireLinearHistory       bool                        `json:"requireLinearHistory,omitempty"`
	AllowForcePushes           bool                        `json:"allowForcePushes,omitempty"`
	AllowDeletions             bool                        `json:"allowDeletions,omitempty"`
}

type RepositoryRuleset struct {
	Name        string             `json:"name,omitempty"`
	Target      string             `json:"target,omitempty"`
	Enforcement string             `json:"enforcement,omitempty"`
	Conditions  *RulesetConditions `json:"conditions,omitempty"`
	Rules       *RulesetRules      `json:"rules,omitempty"`
}

type RulesetConditions struct {
	RefName *RefNameCondition `json:"refName,omitempty"`
}

type RefNameCondition struct {
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

type RulesetRules struct {
	MergeQueue *MergeQueueRule `json:"mergeQueue,omitempty"`
}

type MergeQueueRule struct {
	CheckResponseTimeoutMinutes  int    `json:"checkResponseTimeoutMinutes,omitempty"`
	GroupingStrategy             string `json:"groupingStrategy,omitempty"`
	MaxEntriesToBuild            int    `json:"maxEntriesToBuild,omitempty"`
	MaxEntriesToMerge            int    `json:"maxEntriesToMerge,omitempty"`
	MergeMethod                  string `json:"mergeMethod,omitempty"`
	MinEntriesToMerge            int    `json:"minEntriesToMerge,omitempty"`
	MinEntriesToMergeWaitMinutes int    `json:"minEntriesToMergeWaitMinutes,omitempty"`
}

type RequiredStatusChecks struct {
	Strict   bool     `json:"strict,omitempty"`
	Contexts []string `json:"contexts,omitempty"`
}

type RequiredPullRequestReviews struct {
	DismissStaleReviews          bool `json:"dismissStaleReviews,omitempty"`
	RequireCodeOwnerReviews      bool `json:"requireCodeOwnerReviews,omitempty"`
	RequiredApprovingReviewCount int  `json:"requiredApprovingReviewCount,omitempty"`
}
