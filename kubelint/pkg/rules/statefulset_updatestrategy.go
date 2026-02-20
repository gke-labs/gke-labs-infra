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
	"github.com/gke-labs/gke-labs-infra/kubelint/pkg/manifests"
	"github.com/gke-labs/gke-labs-infra/kubelint/rules"
)

type StatefulSetUpdateStrategy struct {
	name    string
	message string
}

func (r *StatefulSetUpdateStrategy) init() {
	if r.name == "" {
		r.name, r.message = ParseRuleMarkdown(ruledata.StatefulSetUpdateStrategyMD)
	}
}

func (r *StatefulSetUpdateStrategy) Name() string {
	r.init()
	return r.name
}

func (r *StatefulSetUpdateStrategy) Check(obj *manifests.Object) []Diagnostic {
	r.init()
	kind, _, _ := obj.Kind()
	if kind != "StatefulSet" {
		return nil
	}

	_, found, _ := obj.GetString("spec.updateStrategy.type")
	if !found {
		// Also check if spec.updateStrategy is set but type is missing (though type is required if updateStrategy is present)
		_, found, _ = obj.GetString("spec.updateStrategy")
		if !found {
			line, _ := obj.GetLine("kind")
			return []Diagnostic{
				{
					RuleName: r.Name(),
					Message:  r.message,
					Line:     line,
				},
			}
		}
	}

	return nil
}
