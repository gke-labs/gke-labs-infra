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

package unused

import (
	"go/ast"
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"
)

var checkParameters bool

var Analyzer = &analysis.Analyzer{
	Name: "unused",
	Doc:  "check for unused parameters, methods, and fields",
	Run:  run,
}

func init() {
	Analyzer.Flags.BoolVar(&checkParameters, "check-parameters", false, "report unused parameters")
}

func run(pass *analysis.Pass) (interface{}, error) {
	// We use token.Pos as the key because go/types creates different Object instances
	// for different instantiations of a generic type, even though they refer to the same declaration.
	used := make(map[token.Pos]bool)
	for _, obj := range pass.TypesInfo.Uses {
		if obj.Pkg() == pass.Pkg {
			used[obj.Pos()] = true
		}
	}

	for _, f := range pass.Files {
		if isGenerated(f) {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.FuncDecl:
				checkUnusedParams(pass, node.Type.Params, node.Body, used)
				checkUnusedFunc(pass, node, used)
			case *ast.FuncLit:
				checkUnusedParams(pass, node.Type.Params, node.Body, used)
			case *ast.StructType:
				checkUnusedFields(pass, node, used)
			}
			return true
		})
	}
	return nil, nil
}

func checkUnusedParams(pass *analysis.Pass, params *ast.FieldList, body *ast.BlockStmt, used map[token.Pos]bool) {
	if !checkParameters {
		return
	}
	if body == nil || params == nil {
		return
	}
	for _, field := range params.List {
		for _, name := range field.Names {
			if name.Name == "_" {
				continue
			}
			obj := pass.TypesInfo.Defs[name]
			if obj != nil && !used[obj.Pos()] {
				pass.Reportf(name.Pos(), "parameter %s is unused, consider removing or renaming it as _", name.Name)
			}
		}
	}
}

func checkUnusedFunc(pass *analysis.Pass, fn *ast.FuncDecl, used map[token.Pos]bool) {
	name := fn.Name.Name
	if name == "main" || name == "init" || strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Benchmark") || strings.HasPrefix(name, "Example") {
		return
	}
	// Only check unexported functions/methods
	if ast.IsExported(name) {
		return
	}
	obj := pass.TypesInfo.Defs[fn.Name]
	if obj != nil && !used[obj.Pos()] {
		if fn.Recv == nil {
			pass.Reportf(fn.Name.Pos(), "func %s is unused", name)
		} else {
			// For methods, we should be careful about interfaces.
			// But if it's unexported, it can only satisfy unexported interfaces in the same package.
			// For now, let's report it as unused if it's not in Uses.
			pass.Reportf(fn.Name.Pos(), "method %s is unused", name)
		}
	}
}

func checkUnusedFields(pass *analysis.Pass, st *ast.StructType, used map[token.Pos]bool) {
	if st.Fields == nil {
		return
	}
	for _, field := range st.Fields.List {
		for _, name := range field.Names {
			if name.Name == "_" || ast.IsExported(name.Name) {
				continue
			}
			obj := pass.TypesInfo.Defs[name]
			if obj != nil && !used[obj.Pos()] {
				pass.Reportf(name.Pos(), "field %s is unused", name.Name)
			}
		}
	}
}

func isGenerated(f *ast.File) bool {
	for _, comment := range f.Comments {
		for _, c := range comment.List {
			if strings.Contains(c.Text, "Code generated") || strings.Contains(c.Text, "DO NOT EDIT") {
				return true
			}
		}
	}
	return false
}
