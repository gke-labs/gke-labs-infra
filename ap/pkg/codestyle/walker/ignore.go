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

package walker

import "github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"

// IgnoreList matches paths against a list of patterns, similar to .gitignore.
type IgnoreList = sourcetree.IgnoreList

// NewIgnoreList creates a new IgnoreList.
func NewIgnoreList(patterns []string) (*IgnoreList, error) {
	return sourcetree.NewIgnoreList(patterns)
}
