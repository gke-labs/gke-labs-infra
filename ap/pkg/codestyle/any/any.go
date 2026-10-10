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

package any

import (
	"go/ast"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/fileset"
	"golang.org/x/tools/go/analysis"
)

var Analyzer = &analysis.Analyzer{
	Name: "replaceEmptyInterfaceWithAny",
	Doc:  "check for use of interface{} where any could be used",
	Run:  run,
}

var policyFlags *fileset.PolicyFlags

func init() {
	policyFlags = fileset.RegisterFlags(Analyzer, true, false)
}

func run(pass *analysis.Pass) (any, error) {
	policy := policyFlags.NewPolicy()

	for _, f := range pass.Files {
		if policy.ShouldSkipAST(pass.Fset, f) {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if it, ok := n.(*ast.InterfaceType); ok {
				if it.Methods == nil || len(it.Methods.List) == 0 {
					pass.Reportf(it.Pos(), "use any instead of interface{}")
				}
			}
			return true
		})
	}
	return nil, nil
}
