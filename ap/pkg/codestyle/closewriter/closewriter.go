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

package closewriter

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/fileset"
	"golang.org/x/tools/go/analysis"
)

// DefaultTypes is the default list of writer types whose Close (or Flush) flushes.
var DefaultTypes = []string{
	"*bufio.Writer",
	"*compress/gzip.Writer",
	"*compress/zlib.Writer",
	"*archive/tar.Writer",
	"*archive/zip.Writer",
	"*encoding/csv.Writer",
	"*io.PipeWriter",
	"io.WriteCloser",
}

// Config holds configuration options for the closewriter analyzer.
type Config struct {
	fileset.Policy
	Types []string
}

// NewAnalyzer creates a closewriter analyzer configured with cfg.
func NewAnalyzer(cfg Config) *analysis.Analyzer {
	return &analysis.Analyzer{
		Name: "closewriter",
		Doc:  "check for Close() errors on writers dropped or only logged",
		Run: func(pass *analysis.Pass) (any, error) {
			return run(pass, cfg)
		},
	}
}

func run(pass *analysis.Pass, cfg Config) (any, error) {
	configuredTypes := DefaultTypes
	if len(cfg.Types) > 0 {
		configuredTypes = cfg.Types
	}

	for _, file := range pass.Files {
		if cfg.Policy.ShouldSkipAST(pass.Fset, file) {
			continue
		}

		checkFile(file, pass, configuredTypes)
	}

	return nil, nil
}

func checkFile(file *ast.File, pass *analysis.Pass, configuredTypes []string) {
	for _, decl := range file.Decls {
		fnDecl, ok := decl.(*ast.FuncDecl)
		if !ok || fnDecl.Body == nil {
			continue
		}
		checkFunction(fnDecl, fnDecl.Body, formatEnclosingFunc(fnDecl), pass, configuredTypes)
	}

	// Also check package-level variable declarations initialized with function literals
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.VAR {
			continue
		}
		for _, spec := range genDecl.Specs {
			valSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, val := range valSpec.Values {
				fnLit, ok := val.(*ast.FuncLit)
				if !ok || fnLit.Body == nil {
					continue
				}
				name := "<func>"
				if i < len(valSpec.Names) {
					name = valSpec.Names[i].Name
				}
				checkFunction(fnLit, fnLit.Body, name, pass, configuredTypes)
			}
		}
	}
}

func formatEnclosingFunc(fn *ast.FuncDecl) string {
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		recv := fn.Recv.List[0].Type
		if star, ok := recv.(*ast.StarExpr); ok {
			return fmt.Sprintf("(*%s).%s", types.ExprString(star.X), fn.Name.Name)
		}
		return fmt.Sprintf("(%s).%s", types.ExprString(recv), fn.Name.Name)
	}
	return fn.Name.Name
}

func checkFunction(fnNode ast.Node, body *ast.BlockStmt, encFuncName string, pass *analysis.Pass, configuredTypes []string) {
	parentMap := make(map[ast.Node]ast.Node)
	buildParentMap(body, fnNode, parentMap)

	// Collect named return variables of the enclosing function
	namedReturns := make(map[*types.Var]bool)
	if fnDecl, ok := fnNode.(*ast.FuncDecl); ok && fnDecl.Type != nil && fnDecl.Type.Results != nil {
		for _, field := range fnDecl.Type.Results.List {
			for _, name := range field.Names {
				if obj := pass.TypesInfo.Defs[name]; obj != nil {
					if v, ok := obj.(*types.Var); ok {
						namedReturns[v] = true
					}
				}
			}
		}
	} else if fnLit, ok := fnNode.(*ast.FuncLit); ok && fnLit.Type != nil && fnLit.Type.Results != nil {
		for _, field := range fnLit.Type.Results.List {
			for _, name := range field.Names {
				if obj := pass.TypesInfo.Defs[name]; obj != nil {
					if v, ok := obj.(*types.Var); ok {
						namedReturns[v] = true
					}
				}
			}
		}
	}

	// Classify local variables and values
	provenanceMap := make(map[*types.Var]provenance)
	findProvenances(body, pass.TypesInfo, provenanceMap)

	// Find all candidate Close() (or Flush()) calls on writers
	var closeCalls []*ast.CallExpr
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		methodName := sel.Sel.Name
		recv := sel.X

		if !isWriterCall(methodName, recv, body, pass.TypesInfo, provenanceMap, configuredTypes) {
			return true
		}

		closeCalls = append(closeCalls, call)
		return true
	})

	for _, call := range closeCalls {
		sel := call.Fun.(*ast.SelectorExpr)
		recv := sel.X
		methodName := sel.Sel.Name

		// Check if error is handled
		if isErrorHandled(call, fnNode, parentMap, namedReturns, pass.TypesInfo) {
			continue
		}

		// Check close-twice idiom: deferred best-effort Close with explicit checked Close elsewhere
		if isInDefer(call, parentMap) && hasCheckedCloseElsewhere(call, recv, closeCalls, fnNode, parentMap, namedReturns, pass.TypesInfo) {
			continue
		}

		// Only report if the error was actually logged (not dropped outright).
		// Dropped-outright errors (e.g. defer f.Close() or _ = f.Close()) are handled by droppederrors.
		if !isErrorOnlyLogged(call, fnNode, parentMap, pass.TypesInfo) {
			continue
		}

		msg := fmt.Sprintf("error from %s() on a writer is only logged in %s; return it so write failures are not silent", methodName, encFuncName)
		pass.Report(analysis.Diagnostic{
			Pos:      call.Pos(),
			Message:  msg,
			Category: "closewriter",
		})
	}
}

func buildParentMap(node ast.Node, parent ast.Node, parentMap map[ast.Node]ast.Node) {
	if node == nil {
		return
	}
	if parent != nil {
		parentMap[node] = parent
	}
	ast.Inspect(node, func(child ast.Node) bool {
		if child != nil && child != node {
			buildParentMap(child, node, parentMap)
			return false
		}
		return true
	})
}

type provenance int

const (
	provUnknown provenance = iota
	provReader             // e.g. os.Open or os.OpenFile with O_RDONLY
	provWriter             // e.g. os.Create, os.CreateTemp, os.OpenFile with O_WRONLY/O_RDWR/O_APPEND
)

func findProvenances(body ast.Node, info *types.Info, provMap map[*types.Var]provenance) {
	ast.Inspect(body, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range stmt.Lhs {
				id, ok := unwrapIdent(lhs)
				if !ok {
					continue
				}
				v := lookupVar(id, info)
				if v == nil {
					continue
				}
				var rhs ast.Expr
				if i < len(stmt.Rhs) {
					rhs = stmt.Rhs[i]
				} else if len(stmt.Rhs) == 1 {
					rhs = stmt.Rhs[0]
				}
				if rhs != nil {
					p := determineCallProvenance(rhs, info)
					if p != provUnknown {
						provMap[v] = p
					}
				}
			}
		case *ast.ValueSpec:
			for i, name := range stmt.Names {
				v := lookupVar(name, info)
				if v == nil {
					continue
				}
				if i < len(stmt.Values) {
					p := determineCallProvenance(stmt.Values[i], info)
					if p != provUnknown {
						provMap[v] = p
					}
				}
			}
		}
		return true
	})
}

func determineCallProvenance(expr ast.Expr, info *types.Info) provenance {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return provUnknown
	}

	fnName, pkgPath := resolveCallee(call.Fun, info)
	if pkgPath == "os" {
		switch fnName {
		case "Open":
			return provReader
		case "Create", "CreateTemp":
			return provWriter
		case "OpenFile":
			if len(call.Args) >= 2 {
				if hasWriteFlag(call.Args[1]) {
					return provWriter
				}
				return provReader
			}
		}
	}
	return provUnknown
}

func hasWriteFlag(flagExpr ast.Expr) bool {
	hasWrite := false
	ast.Inspect(flagExpr, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			switch id.Name {
			case "O_WRONLY", "O_RDWR", "O_APPEND":
				hasWrite = true
			}
		}
		return true
	})
	return hasWrite
}

func resolveCallee(fun ast.Expr, info *types.Info) (name string, pkgPath string) {
	switch f := fun.(type) {
	case *ast.SelectorExpr:
		if id, ok := f.X.(*ast.Ident); ok {
			if pkgObj, ok := info.Uses[id]; ok {
				if pkgName, ok := pkgObj.(*types.PkgName); ok {
					return f.Sel.Name, pkgName.Imported().Path()
				}
			}
			// AST fallback
			return f.Sel.Name, id.Name
		}
	case *ast.Ident:
		if obj, ok := info.Uses[f]; ok {
			if obj.Pkg() != nil {
				return f.Name, obj.Pkg().Path()
			}
		}
		return f.Name, ""
	}
	return "", ""
}

func isWriterCall(methodName string, recv ast.Expr, body ast.Node, info *types.Info, provMap map[*types.Var]provenance, configuredTypes []string) bool {
	recvType := info.TypeOf(recv)
	if recvType == nil {
		return false
	}

	typStr := recvType.String()

	// Check if matches configured flush types
	isConfiguredType, targetMethod := matchesConfiguredType(recvType, typStr, configuredTypes)
	if isConfiguredType {
		if targetMethod != "" {
			return methodName == targetMethod
		}
		return methodName == "Close" || methodName == "CloseWithError"
	}

	// Check *os.File
	if isOsFile(recvType) {
		if methodName != "Close" {
			return false
		}

		v := getReceiverVar(recv, info)
		if v != nil {
			if p, ok := provMap[v]; ok {
				if p == provReader {
					return false
				}
				if p == provWriter {
					return true
				}
			}
		}

		// Unknown provenance (parameter, field, or unassigned file):
		// treat as writer only if the function also calls Write/WriteString/io.Copy(recv, ...) on it.
		return hasWriteCallsOn(recv, body, info)
	}

	return false
}

func matchesConfiguredType(t types.Type, typStr string, configuredTypes []string) (bool, string) {
	for _, pattern := range configuredTypes {
		cleanPattern := strings.TrimSpace(pattern)
		// Extract method requirement if pattern includes it, e.g. "*bufio.Writer" -> Flush
		if cleanPattern == "*bufio.Writer" || cleanPattern == "bufio.Writer" {
			if typStr == "*bufio.Writer" || typStr == "bufio.Writer" {
				return true, "Flush"
			}
		}

		if typStr == cleanPattern || strings.TrimPrefix(typStr, "*") == strings.TrimPrefix(cleanPattern, "*") {
			return true, "Close"
		}

		// Check interface implementation (e.g. io.WriteCloser)
		if cleanPattern == "io.WriteCloser" {
			if typStr == "io.WriteCloser" || typStr == "*io.PipeWriter" {
				return true, "Close"
			}
		}
	}
	return false, ""
}

func isOsFile(t types.Type) bool {
	if t == nil {
		return false
	}
	return t.String() == "*os.File"
}

func getReceiverVar(recv ast.Expr, info *types.Info) *types.Var {
	recv = unwrapParen(recv)
	if id, ok := recv.(*ast.Ident); ok {
		return lookupVar(id, info)
	}
	return nil
}

func hasWriteCallsOn(recv ast.Expr, body ast.Node, info *types.Info) bool {
	targetVar := getReceiverVar(recv, info)
	foundWrite := false

	ast.Inspect(body, func(n ast.Node) bool {
		if foundWrite {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		// Check method call: recv.Write(...) or recv.WriteString(...)
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if sel.Sel.Name == "Write" || sel.Sel.Name == "WriteString" {
				if sameReceiver(sel.X, recv, targetVar, info) {
					foundWrite = true
					return false
				}
			}
		}

		// Check io.Copy(recv, ...), io.CopyBuffer(recv, ...), io.CopyN(recv, ...)
		fnName, pkgPath := resolveCallee(call.Fun, info)
		if pkgPath == "io" && (fnName == "Copy" || fnName == "CopyBuffer" || fnName == "CopyN") {
			if len(call.Args) > 0 && sameReceiver(call.Args[0], recv, targetVar, info) {
				foundWrite = true
				return false
			}
		}

		return true
	})

	return foundWrite
}

func sameReceiver(r1, r2 ast.Expr, targetVar *types.Var, info *types.Info) bool {
	r1 = unwrapParen(r1)
	r2 = unwrapParen(r2)

	if targetVar != nil {
		if v1 := getReceiverVar(r1, info); v1 != nil && v1 == targetVar {
			return true
		}
	}

	id1, ok1 := r1.(*ast.Ident)
	id2, ok2 := r2.(*ast.Ident)
	if ok1 && ok2 {
		obj1 := info.Uses[id1]
		obj2 := info.Uses[id2]
		if obj1 != nil && obj2 != nil && obj1 == obj2 {
			return true
		}
		return id1.Name == id2.Name
	}

	sel1, ok1 := r1.(*ast.SelectorExpr)
	sel2, ok2 := r2.(*ast.SelectorExpr)
	if ok1 && ok2 {
		if sel1.Sel.Name == sel2.Sel.Name {
			return sameReceiver(sel1.X, sel2.X, nil, info)
		}
	}

	return false
}

func unwrapParen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

func unwrapIdent(e ast.Expr) (*ast.Ident, bool) {
	e = unwrapParen(e)
	id, ok := e.(*ast.Ident)
	return id, ok
}

func lookupVar(id *ast.Ident, info *types.Info) *types.Var {
	if obj, ok := info.Defs[id]; ok {
		if v, ok := obj.(*types.Var); ok {
			return v
		}
	}
	if obj, ok := info.Uses[id]; ok {
		if v, ok := obj.(*types.Var); ok {
			return v
		}
	}
	return nil
}

func isInDefer(n ast.Node, parentMap map[ast.Node]ast.Node) bool {
	curr := n
	for curr != nil {
		parent := parentMap[curr]
		if parent == nil {
			break
		}
		if _, ok := parent.(*ast.DeferStmt); ok {
			return true
		}
		curr = parent
	}
	return false
}

func hasCheckedCloseElsewhere(call *ast.CallExpr, recv ast.Expr, allCloseCalls []*ast.CallExpr, fnNode ast.Node, parentMap map[ast.Node]ast.Node, namedReturns map[*types.Var]bool, info *types.Info) bool {
	targetVar := getReceiverVar(recv, info)

	for _, other := range allCloseCalls {
		if other == call {
			continue
		}
		if isInDefer(other, parentMap) {
			continue
		}
		otherSel, ok := other.Fun.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		if !sameReceiver(otherSel.X, recv, targetVar, info) {
			continue
		}
		if isErrorHandled(other, fnNode, parentMap, namedReturns, info) {
			return true
		}
	}
	return false
}

func isErrorHandled(call *ast.CallExpr, fnNode ast.Node, parentMap map[ast.Node]ast.Node, namedReturns map[*types.Var]bool, info *types.Info) bool {
	parent := parentMap[call]
	if parent == nil {
		return false
	}

	// 1. Direct return: return f.Close()
	if _, ok := parent.(*ast.ReturnStmt); ok {
		return true
	}

	// 2. Direct process-terminating call: panic(f.Close()), log.Fatal(f.Close()), etc.
	if callParent, ok := parent.(*ast.CallExpr); ok {
		if isProcessTerminatingCall(callParent, info) {
			return true
		}
		// 3. Test failure: t.Fatal(f.Close()), t.Error(f.Close())
		if isTestFailureCall(callParent, info) {
			return true
		}
		// 4. errors.Join(..., f.Close(), ...)
		fnName, pkgPath := resolveCallee(callParent.Fun, info)
		if pkgPath == "errors" && fnName == "Join" {
			joinParent := parentMap[callParent]
			if isSinkHandled(joinParent, namedReturns, parentMap, info) {
				return true
			}
		}
		// fmt.Errorf("...: %w", f.Close())
		if pkgPath == "fmt" && fnName == "Errorf" {
			fmtParent := parentMap[callParent]
			if isSinkHandled(fmtParent, namedReturns, parentMap, info) {
				return true
			}
		}
	}

	// 5. Assigned to a variable or named return: cerr := f.Close() or retErr = f.Close()
	switch p := parent.(type) {
	case *ast.AssignStmt:
		for i, rhs := range p.Rhs {
			if rhs != call {
				continue
			}
			if i < len(p.Lhs) {
				lhs := p.Lhs[i]
				if id, ok := unwrapIdent(lhs); ok {
					// Check if assigned directly to a named return
					if v := lookupVar(id, info); v != nil && namedReturns[v] {
						return true
					}
					// Check uses of the local variable
					if v := lookupVar(id, info); v != nil {
						if isVarHandled(v, fnNode, parentMap, namedReturns, info, nil) {
							return true
						}
					}
				}
			}
		}
	case *ast.ValueSpec:
		for i, val := range p.Values {
			if val != call {
				continue
			}
			if i < len(p.Names) {
				name := p.Names[i]
				if v := lookupVar(name, info); v != nil && namedReturns[v] {
					return true
				}
				if v := lookupVar(name, info); v != nil {
					if isVarHandled(v, fnNode, parentMap, namedReturns, info, nil) {
						return true
					}
				}
			}
		}
	}

	return false
}

func isSinkHandled(node ast.Node, namedReturns map[*types.Var]bool, parentMap map[ast.Node]ast.Node, info *types.Info) bool {
	if node == nil {
		return false
	}
	if _, ok := node.(*ast.ReturnStmt); ok {
		return true
	}
	if call, ok := node.(*ast.CallExpr); ok && isProcessTerminatingCall(call, info) {
		return true
	}
	if assign, ok := node.(*ast.AssignStmt); ok {
		for _, lhs := range assign.Lhs {
			if id, ok := unwrapIdent(lhs); ok {
				if v := lookupVar(id, info); v != nil && namedReturns[v] {
					return true
				}
			}
		}
	}
	return false
}

type varUseKey struct {
	v *types.Var
	u *ast.Ident
}

func isVarHandled(v *types.Var, fnNode ast.Node, parentMap map[ast.Node]ast.Node, namedReturns map[*types.Var]bool, info *types.Info, visited map[varUseKey]bool) bool {
	if visited == nil {
		visited = make(map[varUseKey]bool)
	}

	// Find all uses of v within fnNode
	var uses []*ast.Ident
	ast.Inspect(fnNode, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if obj, ok := info.Uses[id]; ok && obj == v {
			uses = append(uses, id)
		}
		return true
	})

	if len(uses) == 0 {
		return false
	}

	for _, u := range uses {
		key := varUseKey{v: v, u: u}
		if visited[key] {
			continue
		}
		visited[key] = true
		if isUseReachingCaller(u, fnNode, parentMap, namedReturns, info, visited) {
			return true
		}
	}
	return false
}

func isUseReachingCaller(id *ast.Ident, fnNode ast.Node, parentMap map[ast.Node]ast.Node, namedReturns map[*types.Var]bool, info *types.Info, visited map[varUseKey]bool) bool {
	parent := parentMap[id]
	if parent == nil {
		return false
	}

	// Direct return: return cerr
	if _, ok := parent.(*ast.ReturnStmt); ok {
		return true
	}

	// In a CallExpr:
	if call, ok := parent.(*ast.CallExpr); ok {
		// panic(cerr), os.Exit(...), log.Fatalf(...), klog.Fatalf(...), etc.
		if isProcessTerminatingCall(call, info) {
			return true
		}
		// t.Fatal(cerr), t.Error(cerr)
		if isTestFailureCall(call, info) {
			return true
		}
		// fmt.Errorf("...", cerr) or errors.Join(..., cerr)
		fnName, pkgPath := resolveCallee(call.Fun, info)
		if (pkgPath == "fmt" && fnName == "Errorf") || (pkgPath == "errors" && fnName == "Join") {
			callParent := parentMap[call]
			if isSinkHandled(callParent, namedReturns, parentMap, info) {
				return true
			}
			// What if call is assigned to another local var: wrappedErr := fmt.Errorf(...); return wrappedErr
			if assign, ok := callParent.(*ast.AssignStmt); ok {
				for i, rhs := range assign.Rhs {
					if unwrapParen(rhs) == call {
						if i < len(assign.Lhs) {
							if lhsId, ok := unwrapIdent(assign.Lhs[i]); ok {
								if v2 := lookupVar(lhsId, info); v2 != nil {
									if isVarHandled(v2, fnNode, parentMap, namedReturns, info, visited) {
										return true
									}
								}
							}
						}
					}
				}
			}
			if valSpec, ok := callParent.(*ast.ValueSpec); ok {
				for i, val := range valSpec.Values {
					if unwrapParen(val) == call {
						if i < len(valSpec.Names) {
							if v2 := lookupVar(valSpec.Names[i], info); v2 != nil {
								if isVarHandled(v2, fnNode, parentMap, namedReturns, info, visited) {
									return true
								}
							}
						}
					}
				}
			}
		}
	}

	// In an AssignStmt: retErr = cerr or err = errors.Join(err, cerr)
	if assign, ok := parent.(*ast.AssignStmt); ok {
		for i, rhs := range assign.Rhs {
			if unwrapParen(rhs) == id {
				if i < len(assign.Lhs) {
					if lhsId, ok := unwrapIdent(assign.Lhs[i]); ok {
						if v := lookupVar(lhsId, info); v != nil && namedReturns[v] {
							return true
						}
						// Also if assigned to local error variable that is later returned:
						// err = cerr; return err
						if v := lookupVar(lhsId, info); v != nil {
							if isVarHandled(v, fnNode, parentMap, namedReturns, info, visited) {
								return true
							}
						}
					}
				}
			}
		}
	}

	// In a ValueSpec: var retErr = cerr
	if valSpec, ok := parent.(*ast.ValueSpec); ok {
		for i, val := range valSpec.Values {
			if unwrapParen(val) == id {
				if i < len(valSpec.Names) {
					if v := lookupVar(valSpec.Names[i], info); v != nil && namedReturns[v] {
						return true
					}
					if v := lookupVar(valSpec.Names[i], info); v != nil {
						if isVarHandled(v, fnNode, parentMap, namedReturns, info, visited) {
							return true
						}
					}
				}
			}
		}
	}

	// Check if this use occurs within an if-statement or closure whose block terminates the process
	if ifStmt := findEnclosingIfStmt(id, parentMap); ifStmt != nil {
		if blockContainsTerminatingCall(ifStmt.Body, info) {
			return true
		}
	}
	if fnLit := findEnclosingFuncLit(id, parentMap); fnLit != nil {
		if blockContainsTerminatingCall(fnLit.Body, info) {
			return true
		}
	}

	return false
}

func findEnclosingIfStmt(n ast.Node, parentMap map[ast.Node]ast.Node) *ast.IfStmt {
	curr := n
	for curr != nil {
		parent := parentMap[curr]
		if parent == nil {
			break
		}
		if ifStmt, ok := parent.(*ast.IfStmt); ok {
			return ifStmt
		}
		curr = parent
	}
	return nil
}

func findEnclosingFuncLit(n ast.Node, parentMap map[ast.Node]ast.Node) *ast.FuncLit {
	curr := n
	for curr != nil {
		parent := parentMap[curr]
		if parent == nil {
			break
		}
		if fnLit, ok := parent.(*ast.FuncLit); ok {
			return fnLit
		}
		curr = parent
	}
	return nil
}

func blockContainsTerminatingCall(n ast.Node, info *types.Info) bool {
	if n == nil {
		return false
	}
	found := false
	ast.Inspect(n, func(node ast.Node) bool {
		if found {
			return false
		}
		if call, ok := node.(*ast.CallExpr); ok {
			if isProcessTerminatingCall(call, info) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func isProcessTerminatingCall(call *ast.CallExpr, info *types.Info) bool {
	if call == nil {
		return false
	}

	// panic(...)
	if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "panic" {
		return true
	}

	// os.Exit(...)
	fnName, pkgPath := resolveCallee(call.Fun, info)
	if pkgPath == "os" && fnName == "Exit" {
		return true
	}

	// log.Fatal*, log.Panic*
	if pkgPath == "log" && (strings.HasPrefix(fnName, "Fatal") || strings.HasPrefix(fnName, "Panic")) {
		return true
	}

	// klog.Fatal*, klog.Exit*
	if (pkgPath == "k8s.io/klog" || pkgPath == "k8s.io/klog/v2") && (strings.HasPrefix(fnName, "Fatal") || strings.HasPrefix(fnName, "Exit")) {
		return true
	}

	// Methods on loggers or verbose:
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		name := sel.Sel.Name
		if strings.HasPrefix(name, "Fatal") || strings.HasPrefix(name, "Exit") || strings.HasPrefix(name, "Panic") {
			if recvType := info.TypeOf(sel.X); recvType != nil {
				typStr := recvType.String()
				if strings.Contains(typStr, "klog") || strings.Contains(typStr, "log") || strings.Contains(typStr, "slog") || strings.Contains(typStr, "Logger") {
					return true
				}
			}
			// AST fallback
			if id, ok := unwrapIdent(sel.X); ok {
				switch id.Name {
				case "log", "klog", "slog", "os":
					return true
				}
			}
			if innerCall, ok := sel.X.(*ast.CallExpr); ok {
				if innerSel, ok := innerCall.Fun.(*ast.SelectorExpr); ok {
					if id, ok := unwrapIdent(innerSel.X); ok && (id.Name == "klog" || id.Name == "slog" || id.Name == "log") {
						return true
					}
				}
			}
		}
	}
	return false
}

func isLogCall(call *ast.CallExpr, info *types.Info) bool {
	if call == nil {
		return false
	}

	// Process-terminating calls (Fatal*, Exit*, panic) are not mere logging
	if isProcessTerminatingCall(call, info) {
		return false
	}

	fnName, pkgPath := resolveCallee(call.Fun, info)
	if pkgPath == "k8s.io/klog" || pkgPath == "k8s.io/klog/v2" {
		return true
	}
	if pkgPath == "log" || pkgPath == "log/slog" {
		return true
	}
	if pkgPath == "fmt" && (fnName == "Fprint" || fnName == "Fprintf" || fnName == "Fprintln") {
		if len(call.Args) > 0 && isStderr(call.Args[0], info) {
			return true
		}
	}

	// Selector method fallback
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if recvType := info.TypeOf(sel.X); recvType != nil {
			typStr := recvType.String()
			if strings.Contains(typStr, "klog") || strings.Contains(typStr, "log") || strings.Contains(typStr, "slog") || strings.Contains(typStr, "Logger") {
				return true
			}
		}
		if id, ok := unwrapIdent(sel.X); ok {
			switch id.Name {
			case "klog", "log", "slog":
				return true
			case "fmt":
				if sel.Sel.Name == "Fprint" || sel.Sel.Name == "Fprintf" || sel.Sel.Name == "Fprintln" {
					if len(call.Args) > 0 && isStderr(call.Args[0], info) {
						return true
					}
				}
			}
		}
		if innerCall, ok := sel.X.(*ast.CallExpr); ok {
			if innerSel, ok := innerCall.Fun.(*ast.SelectorExpr); ok {
				if id, ok := unwrapIdent(innerSel.X); ok && (id.Name == "klog" || id.Name == "slog" || id.Name == "log") {
					return true
				}
			}
		}
	}

	return false
}

func isStderr(arg ast.Expr, info *types.Info) bool {
	arg = unwrapParen(arg)
	if sel, ok := arg.(*ast.SelectorExpr); ok {
		if id, ok := unwrapIdent(sel.X); ok && id.Name == "os" && sel.Sel.Name == "Stderr" {
			return true
		}
		if obj := info.Uses[sel.Sel]; obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == "os" && obj.Name() == "Stderr" {
			return true
		}
	}
	if id, ok := unwrapIdent(arg); ok {
		if obj := info.Uses[id]; obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == "os" && obj.Name() == "Stderr" {
			return true
		}
	}
	return false
}

func isErrorOnlyLogged(call *ast.CallExpr, fnNode ast.Node, parentMap map[ast.Node]ast.Node, info *types.Info) bool {
	parent := parentMap[call]
	if parent == nil {
		return false
	}

	// Direct call inside a log call: klog.Infof("close: %v", f.Close())
	if callParent, ok := parent.(*ast.CallExpr); ok {
		if isLogCall(callParent, info) {
			return true
		}
	}

	// Assigned to a variable: cerr := f.Close() or var cerr = f.Close()
	switch p := parent.(type) {
	case *ast.AssignStmt:
		for i, rhs := range p.Rhs {
			if rhs != call {
				continue
			}
			if i < len(p.Lhs) {
				lhs := p.Lhs[i]
				if id, ok := unwrapIdent(lhs); ok && id.Name != "_" {
					if v := lookupVar(id, info); v != nil {
						return isVarLogged(v, fnNode, parentMap, info)
					}
				}
			}
		}
	case *ast.ValueSpec:
		for i, val := range p.Values {
			if val != call {
				continue
			}
			if i < len(p.Names) {
				name := p.Names[i]
				if id, ok := unwrapIdent(name); ok && id.Name != "_" {
					if v := lookupVar(id, info); v != nil {
						return isVarLogged(v, fnNode, parentMap, info)
					}
				}
			}
		}
	}

	return false
}

func isVarLogged(v *types.Var, fnNode ast.Node, parentMap map[ast.Node]ast.Node, info *types.Info) bool {
	var uses []*ast.Ident
	ast.Inspect(fnNode, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if obj, ok := info.Uses[id]; ok && obj == v {
			uses = append(uses, id)
		}
		return true
	})

	for _, u := range uses {
		curr := parentMap[u]
		// Handle err.Error() inside log call
		if sel, ok := curr.(*ast.SelectorExpr); ok && sel.Sel.Name == "Error" {
			curr = parentMap[curr]
		}
		if call, ok := curr.(*ast.CallExpr); ok {
			if isLogCall(call, info) {
				return true
			}
		}
	}
	return false
}

func isTestFailureCall(call *ast.CallExpr, info *types.Info) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "Fatal", "Fatalf", "Error", "Errorf", "FailNow":
		recvType := info.TypeOf(sel.X)
		if recvType != nil {
			typeStr := recvType.String()
			if strings.Contains(typeStr, "testing.T") || strings.Contains(typeStr, "testing.B") || strings.Contains(typeStr, "testing.TB") {
				return true
			}
		}
		// AST fallback
		if id, ok := unwrapIdent(sel.X); ok {
			switch id.Name {
			case "t", "b", "tb":
				return true
			}
		}
	}
	return false
}
