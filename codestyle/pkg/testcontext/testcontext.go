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
	"strings"

	"golang.org/x/tools/go/analysis"
)

var Analyzer = &analysis.Analyzer{
	Name: "testcontext",
	Doc:  "check for context.Background() and context.TODO() in tests, suggesting t.Context() instead",
	Run:  run,
}

func run(pass *analysis.Pass) (interface{}, error) {
	for _, f := range pass.Files {
		isTestFile := strings.HasSuffix(pass.Fset.File(f.Pos()).Name(), "_test.go")

		v := &visitor{
			pass:       pass,
			isTestFile: isTestFile,
		}
		ast.Walk(v, f)
	}
	return nil, nil
}

type visitor struct {
	pass            *analysis.Pass
	isTestFile      bool
	currentFuncHasT bool
}

func (v *visitor) Visit(node ast.Node) ast.Visitor {
	if node == nil {
		return nil
	}

	switch n := node.(type) {
	case *ast.FuncDecl:
		oldHasT := v.currentFuncHasT
		v.currentFuncHasT = hasTestingT(v.pass, n.Type.Params)
		if n.Body != nil {
			ast.Walk(v, n.Body)
		}
		v.currentFuncHasT = oldHasT
		return nil
	case *ast.FuncLit:
		oldHasT := v.currentFuncHasT
		v.currentFuncHasT = hasTestingT(v.pass, n.Type.Params)
		if n.Body != nil {
			ast.Walk(v, n.Body)
		}
		v.currentFuncHasT = oldHasT
		return nil
	case *ast.CallExpr:
		v.checkCall(n)
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
				if v.isTestFile || v.currentFuncHasT {
					v.pass.Reportf(call.Pos(), "consider using t.Context() instead of context.%s()", obj.Name())
				}
			}
		}
	}
}

func hasTestingT(pass *analysis.Pass, params *ast.FieldList) bool {
	if params == nil {
		return false
	}
	for _, field := range params.List {
		if isTestingT(pass, field.Type) {
			return true
		}
	}
	return false
}

func isTestingT(pass *analysis.Pass, expr ast.Expr) bool {
	typ := pass.TypesInfo.TypeOf(expr)
	if typ == nil {
		return false
	}

	s := typ.String()
	// Check for standard testing types
	return s == "*testing.T" || s == "*testing.B" || s == "*testing.F" || s == "testing.TB"
}
