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

package rules

import (
	"strings"

	"github.com/gke-labs/gke-labs-infra/kubelint/pkg/manifests"
)

// ParseRuleMarkdown parses the rule name and short message from the markdown content.
func ParseRuleMarkdown(content string) (string, string) {
	lines := strings.Split(content, "\n")
	var name, message string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") && name == "" {
			name = strings.TrimPrefix(line, "# ")
		} else if line != "" && !strings.HasPrefix(line, "#") && message == "" {
			message = line
		}
		if name != "" && message != "" {
			break
		}
	}
	return name, message
}

// Rule defines a linter rule.
type Rule interface {
	Name() string
	Check(obj *manifests.Object) []Diagnostic
}

// Diagnostic represents a finding by a rule.
type Diagnostic struct {
	Message  string
	Line     int
	RuleName string
}

// AllRules returns all registered rules.
func AllRules() []Rule {
	return []Rule{
		&StatefulSetUpdateStrategy{},
	}
}
