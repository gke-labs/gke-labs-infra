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
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// Analyzer is a linter that detects implicit conversions from Const[T] to *T
// and prevents mutations of objects held behind Const[T].
var Analyzer = &analysis.Analyzer{
	Name: "goconst",
	Doc:  "check for conversions and mutations of Const[T] objects",
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
	return obj.Name() == "Const" && (obj.Pkg() == nil || obj.Pkg().Name() == "goconst" || obj.Pkg().Name() == "a" || strings.HasSuffix(obj.Pkg().Path(), "/goconst"))
}

func originatesFromConst(expr ast.Expr, pass *analysis.Pass, constVars map[types.Object]bool) bool {
	if expr == nil {
		return false
	}
	if typ := pass.TypesInfo.TypeOf(expr); isConstType(typ) {
		return true
	}
	switch e := expr.(type) {
	case *ast.Ident:
		if obj := pass.TypesInfo.ObjectOf(e); obj != nil && constVars[obj] {
			return true
		}
	case *ast.SelectorExpr:
		return originatesFromConst(e.X, pass, constVars)
	case *ast.IndexExpr:
		return originatesFromConst(e.X, pass, constVars)
	case *ast.SliceExpr:
		return originatesFromConst(e.X, pass, constVars)
	case *ast.StarExpr:
		return originatesFromConst(e.X, pass, constVars)
	case *ast.ParenExpr:
		return originatesFromConst(e.X, pass, constVars)
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return originatesFromConst(e.X, pass, constVars)
		}
	}
	return false
}

func isMutatingLhs(expr ast.Expr, pass *analysis.Pass, constVars map[types.Object]bool) (bool, string) {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		if originatesFromConst(e.X, pass, constVars) {
			return true, "cannot assign to field through Const[T]"
		}
	case *ast.IndexExpr:
		if originatesFromConst(e.X, pass, constVars) {
			targetType := pass.TypesInfo.TypeOf(e.X)
			if targetType != nil {
				if _, ok := targetType.Underlying().(*types.Map); ok {
					return true, "cannot assign to map element through Const[T]"
				}
				if _, ok := targetType.Underlying().(*types.Slice); ok {
					return true, "cannot assign to slice element through Const[T]"
				}
			}
			return true, "cannot assign to element through Const[T]"
		}
	case *ast.StarExpr:
		if originatesFromConst(e.X, pass, constVars) {
			return true, "cannot assign to dereferenced Const[T]"
		}
	case *ast.Ident:
		// Direct assignment to an ident aliasing a reachable slice or map
		if obj := pass.TypesInfo.ObjectOf(e); obj != nil && constVars[obj] {
			if typ := obj.Type(); typ != nil {
				if _, ok := typ.Underlying().(*types.Map); ok {
					return true, "cannot assign to map alias through Const[T]"
				}
				if _, ok := typ.Underlying().(*types.Slice); ok {
					return true, "cannot assign to slice alias through Const[T]"
				}
			}
		}
	}
	return false, ""
}

func runAnalyzer(pass *analysis.Pass) (interface{}, error) {
	constVars := make(map[types.Object]bool)

	for _, f := range pass.Files {
		var currentFuncSig *types.Signature

		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.FuncDecl:
				if obj := pass.TypesInfo.ObjectOf(node.Name); obj != nil {
					if sig, ok := obj.Type().(*types.Signature); ok {
						currentFuncSig = sig
					}
				}
			case *ast.FuncLit:
				if typ := pass.TypesInfo.TypeOf(node.Type); typ != nil {
					if sig, ok := typ.(*types.Signature); ok {
						currentFuncSig = sig
					}
				}
			case *ast.AssignStmt:
				for i, lhs := range node.Lhs {
					if isMutating, msg := isMutatingLhs(lhs, pass, constVars); isMutating {
						pass.Reportf(lhs.Pos(), "%s", msg)
					}

					if i < len(node.Rhs) {
						rhs := node.Rhs[i]
						// If assigning to a newly defined variable and RHS originates from Const
						if node.Tok == token.DEFINE && originatesFromConst(rhs, pass, constVars) {
							if ident, ok := lhs.(*ast.Ident); ok {
								if obj := pass.TypesInfo.ObjectOf(ident); obj != nil {
									constVars[obj] = true
								}
							}
						}
						checkConversion(pass, rhs, pass.TypesInfo.TypeOf(lhs))
					}
				}
			case *ast.IncDecStmt:
				if isMutating, _ := isMutatingLhs(node.X, pass, constVars); isMutating {
					pass.Reportf(node.Pos(), "cannot modify value through Const[T]")
				} else if originatesFromConst(node.X, pass, constVars) {
					pass.Reportf(node.Pos(), "cannot modify value through Const[T]")
				}
			case *ast.UnaryExpr:
				if node.Op == token.AND {
					if originatesFromConst(node.X, pass, constVars) {
						pass.Reportf(node.Pos(), "cannot take address through Const[T]")
					}
				}
			case *ast.ValueSpec:
				for i, value := range node.Values {
					if originatesFromConst(value, pass, constVars) {
						if i < len(node.Names) {
							if obj := pass.TypesInfo.ObjectOf(node.Names[i]); obj != nil {
								constVars[obj] = true
							}
						}
					}
					if node.Type != nil {
						targetType := pass.TypesInfo.TypeOf(node.Type)
						checkConversion(pass, value, targetType)
					}
				}
			case *ast.ReturnStmt:
				if currentFuncSig != nil {
					for i, result := range node.Results {
						if i < currentFuncSig.Results().Len() {
							targetType := currentFuncSig.Results().At(i).Type()
							actualType := pass.TypesInfo.TypeOf(result)
							if isConstType(actualType) && !isConstType(targetType) {
								if types.Identical(actualType.Underlying(), targetType.Underlying()) {
									pass.Reportf(result.Pos(), "conversion from Const[T] to *T in return statement")
								}
							}
						}
					}
				}
			case *ast.CallExpr:
				// Check for explicit type conversion: (*T)(c)
				if isTypeExpr(pass, node.Fun) {
					targetType := pass.TypesInfo.TypeOf(node)
					if targetType != nil && !isConstType(targetType) {
						if len(node.Args) == 1 {
							argType := pass.TypesInfo.TypeOf(node.Args[0])
							if isConstType(argType) && types.Identical(argType.Underlying(), targetType.Underlying()) {
								pass.Reportf(node.Pos(), "explicit conversion from Const[T] to *T")
							}
						}
					}
				}

				// Check builtin functions: append, delete, copy
				if ident, ok := node.Fun.(*ast.Ident); ok {
					if b, ok := pass.TypesInfo.ObjectOf(ident).(*types.Builtin); ok {
						switch b.Name() {
						case "delete":
							if len(node.Args) > 0 && originatesFromConst(node.Args[0], pass, constVars) {
								pass.Reportf(node.Pos(), "cannot delete from map through Const[T]")
							}
						case "append":
							if len(node.Args) > 0 && originatesFromConst(node.Args[0], pass, constVars) {
								pass.Reportf(node.Pos(), "cannot append to slice through Const[T]")
							}
						case "copy":
							if len(node.Args) > 0 && originatesFromConst(node.Args[0], pass, constVars) {
								pass.Reportf(node.Pos(), "cannot copy into slice through Const[T]")
							}
						}
					}
				}

				// Check method call with pointer receiver
				if sel, ok := node.Fun.(*ast.SelectorExpr); ok {
					if selection := pass.TypesInfo.Selections[sel]; selection != nil {
						if selection.Kind() == types.MethodVal {
							if fn, ok := selection.Obj().(*types.Func); ok {
								if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
									if _, isPtr := sig.Recv().Type().(*types.Pointer); isPtr {
										if originatesFromConst(sel.X, pass, constVars) {
											pass.Reportf(node.Pos(), "cannot call pointer-receiver method on Const[T]")
										}
									}
								}
							}
						}
					}
				}

				// Check function call arguments
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

func isTypeExpr(pass *analysis.Pass, expr ast.Expr) bool {
	if expr == nil {
		return false
	}
	if tv, ok := pass.TypesInfo.Types[expr]; ok {
		return tv.IsType()
	}
	switch e := expr.(type) {
	case *ast.ParenExpr:
		return isTypeExpr(pass, e.X)
	case *ast.StarExpr:
		return isTypeExpr(pass, e.X)
	}
	return false
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
