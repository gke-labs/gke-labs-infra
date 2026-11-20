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

package testcontext

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/fileset"
	"golang.org/x/tools/go/analysis"
)

// NewAnalyzer creates a testcontext analyzer configured with policy.
func NewAnalyzer(policy fileset.Policy) *analysis.Analyzer {
	return &analysis.Analyzer{
		Name: "testcontext",
		Doc:  "check for context.Background() and context.TODO() in tests, suggesting t.Context() instead",
		Run: func(pass *analysis.Pass) (any, error) {
			return run(pass, policy)
		},
	}
}

func run(pass *analysis.Pass, policy fileset.Policy) (any, error) {
	for _, f := range pass.Files {
		if policy.ShouldSkipAST(pass.Fset, f) {
			continue
		}

		v := &visitor{
			pass: pass,
		}
		ast.Walk(v, f)
	}
	return nil, nil
}

type visitor struct {
	pass *analysis.Pass
}

func (v *visitor) Visit(node ast.Node) ast.Visitor {
	if node == nil {
		return nil
	}

	if call, ok := node.(*ast.CallExpr); ok {
		v.checkCall(call)
	}

	return v
}

func (v *visitor) checkCall(call *ast.CallExpr) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}

	if obj, ok := v.pass.TypesInfo.Uses[sel.Sel]; ok {
		if pkg := obj.Pkg(); pkg != nil && pkg.Path() == "context" {
			if obj.Name() == "Background" || obj.Name() == "TODO" {
				if hasTestingInScope(v.pass, call.Pos()) {
					v.pass.Reportf(call.Pos(), "consider using t.Context() instead of context.%s()", obj.Name())
				}
			}
		}
	}
}

func hasTestingInScope(pass *analysis.Pass, pos token.Pos) bool {
	if pass.Pkg == nil || pass.Pkg.Scope() == nil {
		return false
	}
	inner := pass.Pkg.Scope().Innermost(pos)
	if inner == nil {
		return false
	}

	seen := make(map[string]bool)
	for s := inner; s != nil && s != pass.Pkg.Scope() && s.Parent() != pass.Pkg.Scope(); s = s.Parent() {
		for _, name := range s.Names() {
			if seen[name] {
				continue
			}
			seen[name] = true
			obj := s.Lookup(name)
			if obj == nil {
				continue
			}
			if isTestingType(obj.Type()) {
				return true
			}
		}
	}
	return false
}

func isTestingType(typ types.Type) bool {
	if typ == nil {
		return false
	}
	typ = types.Unalias(typ)
	s := typ.String()
	if s == "*testing.T" || s == "*testing.B" || s == "*testing.F" || s == "testing.TB" {
		return true
	}
	switch t := typ.(type) {
	case *types.Pointer:
		elem := types.Unalias(t.Elem())
		if named, ok := elem.(*types.Named); ok {
			if pkg := named.Obj().Pkg(); pkg != nil && pkg.Path() == "testing" {
				name := named.Obj().Name()
				return name == "T" || name == "B" || name == "F"
			}
		}
	case *types.Named:
		if pkg := t.Obj().Pkg(); pkg != nil && pkg.Path() == "testing" {
			return t.Obj().Name() == "TB"
		}
	}
	return false
}
