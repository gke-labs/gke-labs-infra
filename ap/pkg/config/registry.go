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
	"sync"
)

var (
	registryMu sync.RWMutex
	analyzers  = make(map[string]bool)
)

var wellKnownAnalyzers = []string{
	"closeWriter",
	"deferInLoop",
	"droppedErrors",
	"portability",
	"replaceEmptyInterfaceWithAny",
	"testOnlyImports",
	"testcontext",
	"unused",
	"unusedparameters",
}

// RegisterAnalyzer registers a canonical analyzer name.
func RegisterAnalyzer(name string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	analyzers[name] = true
}

// RegisterLinter registers a canonical analyzer name (alias for RegisterAnalyzer).
func RegisterLinter(name string) {
	RegisterAnalyzer(name)
}

// RegisteredAnalyzers returns a sorted list of registered analyzer names.
func RegisteredAnalyzers() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	if len(analyzers) == 0 {
		out := make([]string, len(wellKnownAnalyzers))
		copy(out, wellKnownAnalyzers)
		sort.Strings(out)
		return out
	}
	names := make([]string, 0, len(analyzers))
	for name := range analyzers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// RegisteredLinters returns a sorted list of registered analyzer names.
func RegisteredLinters() []string {
	return RegisteredAnalyzers()
}

func isRegisteredAnalyzer(name string) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return analyzers[name]
}

func isAnalyzer(name string) bool {
	if isRegisteredAnalyzer(name) {
		return true
	}
	for _, k := range wellKnownAnalyzers {
		if k == name {
			return true
		}
	}
	return false
}
