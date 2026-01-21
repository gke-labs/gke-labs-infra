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

func TestRulesetFromConfig(t *testing.T) {
	targetBranch := github.RulesetTarget("branch")

	tests := []struct {
		name string
		cfg  *config.RepositoryRuleset
		want *github.RepositoryRuleset
	}{
		{
			name: "Basic Ruleset",
			cfg: &config.RepositoryRuleset{
				Name:        "default",
				Target:      "branch",
				Enforcement: "active",
			},
			want: &github.RepositoryRuleset{
				Name:        "default",
				Target:      &targetBranch,
				Enforcement: github.RulesetEnforcement("active"),
			},
		},
		{
			name: "Ruleset with Merge Queue",
			cfg: &config.RepositoryRuleset{
				Name:        "merge-queue",
				Enforcement: "active",
				Rules: &config.RulesetRules{
					MergeQueue: &config.MergeQueueRule{
						MergeMethod:       "SQUASH",
						MinEntriesToMerge: 1,
					},
				},
			},
			want: &github.RepositoryRuleset{
				Name:        "merge-queue",
				Enforcement: "active",
				Rules: &github.RepositoryRulesetRules{
					MergeQueue: &github.MergeQueueRuleParameters{
						MergeMethod:       github.MergeQueueMergeMethod("SQUASH"),
						MinEntriesToMerge: 1,
					},
				},
			},
		},
		{
			name: "Ruleset with Conditions",
			cfg: &config.RepositoryRuleset{
				Name:        "main-protection",
				Enforcement: "active",
				Conditions: &config.RulesetConditions{
					RefName: &config.RefNameCondition{
						Include: []string{"refs/heads/main"},
						Exclude: []string{"refs/heads/dev"},
					},
				},
			},
			want: &github.RepositoryRuleset{
				Name:        "main-protection",
				Enforcement: "active",
				Conditions: &github.RepositoryRulesetConditions{
					RefName: &github.RepositoryRulesetRefConditionParameters{
						Include: []string{"refs/heads/main"},
						Exclude: []string{"refs/heads/dev"},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rulesetFromConfig(tt.cfg)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("rulesetFromConfig() = \n%v\n, want \n%v", got, tt.want)
			}
		})
	}
}
