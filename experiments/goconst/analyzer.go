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

package goconst

import (
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/analysis"
)

// Analyzer is a linter that detects implicit conversions from Const[T] to *T.
var Analyzer = &analysis.Analyzer{
	Name: "goconst",
	Doc:  "check for implicit conversions from Const[T] to *T",
	Run:  runAnalyzer,
}

func isConstType(t types.Type) bool {
	if t == nil {
		return false
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Name() == "Const" && (obj.Pkg() == nil || obj.Pkg().Name() == "goconst" || obj.Pkg().Name() == "a")
}

func runAnalyzer(pass *analysis.Pass) (interface{}, error) {
	for _, f := range pass.Files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.AssignStmt:
				for i, rhs := range node.Rhs {
					if i >= len(node.Lhs) {
						break
					}
					lhs := node.Lhs[i]
					checkConversion(pass, rhs, pass.TypesInfo.TypeOf(lhs))
				}
			case *ast.ValueSpec:
				if node.Type != nil {
					targetType := pass.TypesInfo.TypeOf(node.Type)
					for _, value := range node.Values {
						checkConversion(pass, value, targetType)
					}
				}
			case *ast.ReturnStmt:
				// TODO: Handle return statements if needed.
				// This requires finding the enclosing function's signature.
			case *ast.CallExpr:
				if sig, ok := pass.TypesInfo.TypeOf(node.Fun).(*types.Signature); ok {
					for i, arg := range node.Args {
						var paramType types.Type
						if sig.Variadic() && i >= sig.Params().Len()-1 {
							paramType = sig.Params().At(sig.Params().Len() - 1).Type().(*types.Slice).Elem()
						} else if i < sig.Params().Len() {
							paramType = sig.Params().At(i).Type()
						}
						if paramType != nil {
							checkConversion(pass, arg, paramType)
						}
					}
				}
			case *ast.CompositeLit:
				if typ := pass.TypesInfo.TypeOf(node.Type); typ != nil {
					if structTyp, ok := typ.Underlying().(*types.Struct); ok {
						for _, elt := range node.Elts {
							if kv, ok := elt.(*ast.KeyValueExpr); ok {
								if key, ok := kv.Key.(*ast.Ident); ok {
									for i := 0; i < structTyp.NumFields(); i++ {
										field := structTyp.Field(i)
										if field.Name() == key.Name {
											checkConversion(pass, kv.Value, field.Type())
											break
										}
									}
								}
							}
						}
					} else if mapTyp, ok := typ.Underlying().(*types.Map); ok {
						for _, elt := range node.Elts {
							if kv, ok := elt.(*ast.KeyValueExpr); ok {
								checkConversion(pass, kv.Value, mapTyp.Elem())
							}
						}
					} else if sliceTyp, ok := typ.Underlying().(*types.Slice); ok {
						for _, elt := range node.Elts {
							checkConversion(pass, elt, sliceTyp.Elem())
						}
					}
				}
			}
			return true
		})
	}
	return nil, nil
}

func checkConversion(pass *analysis.Pass, expr ast.Expr, targetType types.Type) {
	if targetType == nil {
		return
	}
	actualType := pass.TypesInfo.TypeOf(expr)
	if actualType == nil {
		return
	}

	if isConstType(actualType) && !isConstType(targetType) {
		if types.Identical(actualType.Underlying(), targetType.Underlying()) {
			pass.Reportf(expr.Pos(), "implicit conversion from Const[T] to *T")
		}
	}
}
