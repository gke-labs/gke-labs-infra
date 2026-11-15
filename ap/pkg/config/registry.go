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

import (
	"sort"
)

var analyzers = make(map[string]bool)

// RegisterAnalyzer registers a canonical analyzer name.
func RegisterAnalyzer(name string) {
	analyzers[name] = true
}

// RegisteredAnalyzers returns a sorted list of registered analyzer names.
func RegisteredAnalyzers() []string {
	names := make([]string, 0, len(analyzers))
	for name := range analyzers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func isAnalyzer(name string) bool {
	return analyzers[name]
}
