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

package tasks

import (
	"path/filepath"
	"strings"
)

// Sourced is an interface for tasks that declare the absolute source path they were discovered from.
type Sourced interface {
	Task
	SourcePath() string
}

// Parent is an interface for composite tasks that can produce a copy with new children.
type Parent interface {
	Task
	WithChildren(children []Task) Task
}

// FilterBySourcePath filters tasks by resolved target paths.
// Leaf tasks are kept if their SourcePath() equals or lies under any target.
// Groups (and other parent tasks) keep only matching children and are dropped when empty.
func FilterBySourcePath(taskList []Task, targets []string) []Task {
	var result []Task
	for _, t := range taskList {
		if filtered := filterTask(t, targets); filtered != nil {
			result = append(result, filtered)
		}
	}
	return result
}

func filterTask(t Task, targets []string) Task {
	if s, ok := t.(Sourced); ok && s.SourcePath() != "" {
		src := s.SourcePath()
		for _, target := range targets {
			if pathEqualsOrUnder(target, src) {
				return t
			}
		}
		return nil
	}

	if p, ok := t.(Parent); ok {
		var matchedChildren []Task
		for _, child := range p.GetChildren() {
			if filteredChild := filterTask(child, targets); filteredChild != nil {
				matchedChildren = append(matchedChildren, filteredChild)
			}
		}
		if len(matchedChildren) == 0 {
			return nil
		}
		return p.WithChildren(matchedChildren)
	}

	return nil
}

func pathEqualsOrUnder(target, sourcePath string) bool {
	if sourcePath == "" || target == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(target), filepath.Clean(sourcePath))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
