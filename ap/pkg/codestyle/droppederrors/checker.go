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

package droppederrors

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/fileset"
	"golang.org/x/tools/go/packages"
)

// Options holds configuration for running the dropped errors checker.
type Options struct {
	RepoRoot           string
	APRoot             string
	Dir                string
	Packages           []string
	Mode               string // ignore | warn | error
	BaselinePath       string
	WriteBaseline      bool
	Exclude            []string
	SkipTests          bool
	SkipGenerated      bool
	SkipGlobs          []string
	UseDefaultExcludes *bool
	GOOS               []string
}

// Result holds the findings and matching results.
type Result struct {
	Findings        []Finding
	NewFindings     []Finding
	StaleEntries    []BaselineEntry
	BaselineEntries []BaselineEntry
	CheckedFiles    map[string]bool
}

var (
	errorType      = types.Universe.Lookup("error").Type()
	errorInterface = errorType.Underlying().(*types.Interface)
)

func isErrorType(t types.Type) bool {
	if t == nil {
		return false
	}
	return types.Implements(t, errorInterface)
}

func isTypeConversion(info *types.Info, call *ast.CallExpr) bool {
	if tv, ok := info.Types[call.Fun]; ok && tv.IsType() {
		return true
	}
	return false
}

func isRecoverCall(call *ast.CallExpr, info *types.Info) bool {
	if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "recover" {
		if obj, ok := info.Uses[id]; ok {
			if b, ok := obj.(*types.Builtin); ok && b.Name() == "recover" {
				return true
			}
		}
	}
	return false
}

func returnErrorIndices(info *types.Info, call *ast.CallExpr) []bool {
	if isTypeConversion(info, call) {
		return nil
	}
	tv, ok := info.Types[call]
	if !ok || tv.Type == nil {
		return nil
	}
	if tuple, ok := tv.Type.(*types.Tuple); ok {
		res := make([]bool, tuple.Len())
		hasErr := false
		for i := 0; i < tuple.Len(); i++ {
			if isErrorType(tuple.At(i).Type()) {
				res[i] = true
				hasErr = true
			}
		}
		if !hasErr {
			return nil
		}
		return res
	}
	if isErrorType(tv.Type) {
		return []bool{true}
	}
	return nil
}

type rawFinding struct {
	pos      token.Pos
	expr     ast.Expr // *ast.CallExpr or other expression
	category string
}

func findDroppedErrorsInFile(file *ast.File, info *types.Info, matcher *ExclusionMatcher) []rawFinding {
	var findings []rawFinding

	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			return true
		}

		switch stmt := n.(type) {
		case *ast.ExprStmt:
			if call, ok := stmt.X.(*ast.CallExpr); ok {
				errIndices := returnErrorIndices(info, call)
				if len(errIndices) > 0 && !matcher.IsExcluded(call, info) {
					findings = append(findings, rawFinding{pos: call.Pos(), expr: call, category: CategoryDroppedError})
				}
			}

		case *ast.GoStmt:
			errIndices := returnErrorIndices(info, stmt.Call)
			if len(errIndices) > 0 && !matcher.IsExcluded(stmt.Call, info) {
				findings = append(findings, rawFinding{pos: stmt.Call.Pos(), expr: stmt.Call, category: CategoryDroppedError})
			}

		case *ast.DeferStmt:
			errIndices := returnErrorIndices(info, stmt.Call)
			if len(errIndices) > 0 && !matcher.IsExcluded(stmt.Call, info) {
				findings = append(findings, rawFinding{pos: stmt.Call.Pos(), expr: stmt.Call, category: CategoryDroppedError})
			}

		case *ast.AssignStmt:
			if len(stmt.Rhs) == 1 {
				if call, ok := stmt.Rhs[0].(*ast.CallExpr); ok {
					isRecv := isRecoverCall(call, info)
					errIndices := returnErrorIndices(info, call)
					if (len(errIndices) > 0 || isRecv) && !matcher.IsExcluded(call, info) {
						for i := 0; i < len(stmt.Lhs); i++ {
							if isRecv || (i < len(errIndices) && errIndices[i]) {
								if id, ok := stmt.Lhs[i].(*ast.Ident); ok && id.Name == "_" {
									findings = append(findings, rawFinding{pos: id.Pos(), expr: call, category: CategoryDroppedError})
								}
							}
						}
					}
				}
			} else if len(stmt.Lhs) == len(stmt.Rhs) {
				for i := range stmt.Lhs {
					if id, ok := stmt.Lhs[i].(*ast.Ident); ok && id.Name == "_" {
						if call, ok := stmt.Rhs[i].(*ast.CallExpr); ok {
							isRecv := isRecoverCall(call, info)
							errIndices := returnErrorIndices(info, call)
							if (isRecv || (len(errIndices) > 0 && errIndices[0])) && !matcher.IsExcluded(call, info) {
								findings = append(findings, rawFinding{pos: id.Pos(), expr: call, category: CategoryDroppedError})
							}
						}
					}
				}
			}

		case *ast.ValueSpec:
			if len(stmt.Values) == 1 {
				if call, ok := stmt.Values[0].(*ast.CallExpr); ok {
					isRecv := isRecoverCall(call, info)
					errIndices := returnErrorIndices(info, call)
					if (len(errIndices) > 0 || isRecv) && !matcher.IsExcluded(call, info) {
						for i := 0; i < len(stmt.Names); i++ {
							if isRecv || (i < len(errIndices) && errIndices[i]) {
								if stmt.Names[i].Name == "_" {
									findings = append(findings, rawFinding{pos: stmt.Names[i].Pos(), expr: call, category: CategoryDroppedError})
								}
							}
						}
					}
				}
			} else if len(stmt.Names) == len(stmt.Values) {
				for i := range stmt.Names {
					if stmt.Names[i].Name == "_" {
						if call, ok := stmt.Values[i].(*ast.CallExpr); ok {
							isRecv := isRecoverCall(call, info)
							errIndices := returnErrorIndices(info, call)
							if (isRecv || (len(errIndices) > 0 && errIndices[0])) && !matcher.IsExcluded(call, info) {
								findings = append(findings, rawFinding{pos: stmt.Names[i].Pos(), expr: call, category: CategoryDroppedError})
							}
						}
					}
				}
			}

		case *ast.RangeStmt:
			checkRangeStmt(stmt, info, matcher, &findings)
		}

		return true
	})

	// Check for "error only tested for success" pattern
	successOnlyFindings := findSuccessOnlyChecks(file, info)
	findings = append(findings, successOnlyFindings...)

	return findings
}

func checkRangeStmt(stmt *ast.RangeStmt, info *types.Info, matcher *ExclusionMatcher, findings *[]rawFinding) {
	if stmt == nil || stmt.X == nil || info == nil {
		return
	}
	tv, ok := info.Types[stmt.X]
	if !ok || tv.Type == nil {
		return
	}
	sig, ok := tv.Type.Underlying().(*types.Signature)
	if !ok || sig.Params().Len() != 1 {
		return
	}
	yieldSig, ok := sig.Params().At(0).Type().Underlying().(*types.Signature)
	if !ok {
		return
	}

	// Seq2: yield(K, V) where V is error
	if yieldSig.Params().Len() == 2 {
		valType := yieldSig.Params().At(1).Type()
		if isErrorType(valType) {
			dropped := false
			if stmt.Value == nil {
				// for k := range seq
				dropped = true
			} else if id, ok := stmt.Value.(*ast.Ident); ok && id.Name == "_" {
				// for k, _ := range seq
				dropped = true
			}
			if dropped {
				*findings = append(*findings, rawFinding{
					pos:      stmt.Pos(),
					expr:     stmt.X,
					category: CategoryDroppedError,
				})
			}
		}
	} else if yieldSig.Params().Len() == 1 {
		// Seq: yield(V) where V is error
		valType := yieldSig.Params().At(0).Type()
		if isErrorType(valType) {
			dropped := false
			if stmt.Key == nil {
				// for range seq
				dropped = true
			} else if id, ok := stmt.Key.(*ast.Ident); ok && id.Name == "_" {
				// for _ := range seq
				dropped = true
			}
			if dropped {
				*findings = append(*findings, rawFinding{
					pos:      stmt.Pos(),
					expr:     stmt.X,
					category: CategoryDroppedError,
				})
			}
		}
	}
}

type errVarDef struct {
	obj  *types.Var
	call *ast.CallExpr
	pos  token.Pos
}

func findSuccessOnlyChecks(file *ast.File, info *types.Info) []rawFinding {
	var errDefs []errVarDef

	// 1. Identify local error variables whose value comes from a call
	ast.Inspect(file, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.AssignStmt:
			if stmt.Tok == token.DEFINE || stmt.Tok == token.ASSIGN {
				if len(stmt.Rhs) == 1 {
					if call, ok := stmt.Rhs[0].(*ast.CallExpr); ok {
						errIndices := returnErrorIndices(info, call)
						for i := range stmt.Lhs {
							if i < len(errIndices) && errIndices[i] {
								if id, ok := stmt.Lhs[i].(*ast.Ident); ok && id.Name != "_" {
									if v, ok := getVarObj(info, id); ok && isErrorType(v.Type()) {
										errDefs = append(errDefs, errVarDef{obj: v, call: call, pos: id.Pos()})
									}
								}
							}
						}
					}
				} else if len(stmt.Lhs) == len(stmt.Rhs) {
					for i := range stmt.Lhs {
						if id, ok := stmt.Lhs[i].(*ast.Ident); ok && id.Name != "_" {
							if call, ok := stmt.Rhs[i].(*ast.CallExpr); ok {
								errIndices := returnErrorIndices(info, call)
								if len(errIndices) > 0 && errIndices[0] {
									if v, ok := getVarObj(info, id); ok && isErrorType(v.Type()) {
										errDefs = append(errDefs, errVarDef{obj: v, call: call, pos: id.Pos()})
									}
								}
							}
						}
					}
				}
			}
		case *ast.ValueSpec:
			if len(stmt.Values) == 1 {
				if call, ok := stmt.Values[0].(*ast.CallExpr); ok {
					errIndices := returnErrorIndices(info, call)
					for i := range stmt.Names {
						if i < len(errIndices) && errIndices[i] && stmt.Names[i].Name != "_" {
							if v, ok := getVarObj(info, stmt.Names[i]); ok && isErrorType(v.Type()) {
								errDefs = append(errDefs, errVarDef{obj: v, call: call, pos: stmt.Names[i].Pos()})
							}
						}
					}
				}
			}
		}
		return true
	})

	if len(errDefs) == 0 {
		return nil
	}

	// 2. Build parent map for file to inspect AST context of uses
	parentMap := make(map[ast.Node]ast.Node)
	var walkParents func(ast.Node, ast.Node)
	walkParents = func(curr, parent ast.Node) {
		if curr == nil {
			return
		}
		if parent != nil {
			parentMap[curr] = parent
		}
		ast.Inspect(curr, func(child ast.Node) bool {
			if child != nil && child != curr {
				walkParents(child, curr)
				return false
			}
			return true
		})
	}
	walkParents(file, nil)

	var findings []rawFinding

	// 3. For each error variable, inspect all uses
	for _, def := range errDefs {
		var uses []*ast.Ident
		ast.Inspect(file, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				if info.Uses[id] == def.obj {
					uses = append(uses, id)
				}
			}
			return true
		})

		if len(uses) == 0 {
			continue
		}

		// Check if every use is operand of `== nil` which is the entire condition of an if with no else
		allSuccessOnly := true
		for _, use := range uses {
			if !isSuccessOnlyUse(use, parentMap) {
				allSuccessOnly = false
				break
			}
		}

		if allSuccessOnly {
			findings = append(findings, rawFinding{
				pos:      def.pos,
				expr:     def.call,
				category: CategoryErrorOnlyCheckedForSuccess,
			})
		}
	}

	return findings
}

func getVarObj(info *types.Info, id *ast.Ident) (*types.Var, bool) {
	if obj, ok := info.Defs[id]; ok {
		if v, ok := obj.(*types.Var); ok {
			return v, true
		}
	}
	if obj, ok := info.Uses[id]; ok {
		if v, ok := obj.(*types.Var); ok {
			return v, true
		}
	}
	return nil, false
}

func isSuccessOnlyUse(use *ast.Ident, parentMap map[ast.Node]ast.Node) bool {
	parent := parentMap[use]
	if paren, ok := parent.(*ast.ParenExpr); ok {
		parent = parentMap[paren]
	}
	bin, ok := parent.(*ast.BinaryExpr)
	if !ok || bin.Op != token.EQL {
		return false
	}

	// Must compare against nil
	if !isNilExpr(bin.X) && !isNilExpr(bin.Y) {
		return false
	}

	grandParent := parentMap[bin]
	if paren, ok := grandParent.(*ast.ParenExpr); ok {
		grandParent = parentMap[paren]
	}

	ifStmt, ok := grandParent.(*ast.IfStmt)
	if !ok {
		return false
	}

	// BinaryExpr must be the entire condition of the if statement
	if ifStmt.Cond != bin {
		return false
	}

	// Must have no else branch
	if ifStmt.Else != nil {
		return false
	}

	return true
}

func isNilExpr(e ast.Expr) bool {
	if id, ok := e.(*ast.Ident); ok && id.Name == "nil" {
		return true
	}
	return false
}

func formatCallee(expr ast.Expr, info *types.Info) string {
	if expr == nil {
		return "<unknown>"
	}
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return types.ExprString(expr)
	}

	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if selection, ok := info.Selections[sel]; ok {
			if fn, ok := selection.Obj().(*types.Func); ok {
				return fn.FullName()
			}
		} else if obj, ok := info.Uses[sel.Sel]; ok {
			if fn, ok := obj.(*types.Func); ok {
				return fn.FullName()
			}
		}
		return types.ExprString(sel)
	}
	if id, ok := call.Fun.(*ast.Ident); ok {
		if obj, ok := info.Uses[id]; ok {
			if fn, ok := obj.(*types.Func); ok {
				return fn.FullName()
			}
		}
		return id.Name
	}
	return types.ExprString(call.Fun)
}

// Run executes the dropped errors checker according to the provided options.
func Run(_ context.Context, opts Options) (*Result, error) {
	if opts.Dir == "" {
		if opts.APRoot != "" {
			opts.Dir = opts.APRoot
		} else if opts.RepoRoot != "" {
			opts.Dir = opts.RepoRoot
		} else {
			var err error
			opts.Dir, err = os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("failed to get current working directory: %w", err)
			}
		}
	}
	if opts.APRoot == "" {
		opts.APRoot = opts.Dir
	}
	if len(opts.Packages) == 0 {
		opts.Packages = []string{"./..."}
	}

	goosList := opts.GOOS
	if len(goosList) == 0 {
		host := runtime.GOOS
		if host == "linux" {
			goosList = []string{"linux"}
		} else {
			goosList = []string{host, "linux"}
		}
	}

	allCheckedFiles := make(map[string]bool)
	findingsByKey := make(map[string]Finding)
	var allFindings []Finding

	useDefaultExcludes := true
	if opts.UseDefaultExcludes != nil {
		useDefaultExcludes = *opts.UseDefaultExcludes
	}
	matcher := NewExclusionMatcher(opts.Exclude, useDefaultExcludes)

	for _, targetGOOS := range goosList {
		findings, checkedFiles, err := checkPackagesForGOOS(opts.Dir, opts.Packages, targetGOOS, opts.SkipTests, opts.SkipGenerated, opts.SkipGlobs, matcher, opts.APRoot, opts.RepoRoot)
		if err != nil {
			return nil, err
		}
		for f := range checkedFiles {
			allCheckedFiles[f] = true
		}
		for _, finding := range findings {
			k := finding.Key()
			if _, exists := findingsByKey[k]; !exists {
				findingsByKey[k] = finding
				allFindings = append(allFindings, finding)
			}
		}
	}

	// Sort findings deterministically by file, line, col, category
	sort.Slice(allFindings, func(i, j int) bool {
		if allFindings[i].RelFile != allFindings[j].RelFile {
			return allFindings[i].RelFile < allFindings[j].RelFile
		}
		if allFindings[i].Pos.Line != allFindings[j].Pos.Line {
			return allFindings[i].Pos.Line < allFindings[j].Pos.Line
		}
		if allFindings[i].Pos.Column != allFindings[j].Pos.Column {
			return allFindings[i].Pos.Column < allFindings[j].Pos.Column
		}
		if allFindings[i].Category != allFindings[j].Category {
			return allFindings[i].Category < allFindings[j].Category
		}
		return allFindings[i].Callee < allFindings[j].Callee
	})

	res := &Result{
		Findings:     allFindings,
		CheckedFiles: allCheckedFiles,
	}

	baselineRoot := opts.RepoRoot
	if baselineRoot == "" {
		baselineRoot = opts.APRoot
	}
	if opts.BaselinePath != "" {
		bDir := filepath.Dir(opts.BaselinePath)
		if filepath.Base(bDir) == ".ap" {
			baselineRoot = filepath.Dir(bDir)
		}
	}

	if opts.WriteBaseline {
		var finalEntries []BaselineEntry
		if opts.BaselinePath != "" {
			existingEntries, err := LoadBaseline(opts.BaselinePath)
			if err != nil {
				return nil, err
			}
			for _, e := range existingEntries {
				entryFile := filepath.ToSlash(e.File)
				if !allCheckedFiles[entryFile] {
					finalEntries = append(finalEntries, e)
				}
			}
		}

		for _, f := range allFindings {
			relFile := relPath(baselineRoot, f.Pos.Filename)
			finalEntries = append(finalEntries, BaselineEntry{
				File:          relFile,
				EnclosingFunc: f.EnclosingFunc,
				Category:      f.Category,
				Callee:        f.Callee,
			})
		}
		if opts.BaselinePath != "" {
			if err := WriteBaseline(opts.BaselinePath, finalEntries); err != nil {
				return nil, fmt.Errorf("failed to write baseline to %s: %w", opts.BaselinePath, err)
			}
		}
		res.BaselineEntries = finalEntries
		return res, nil
	}

	var baselineEntries []BaselineEntry
	if opts.BaselinePath != "" {
		var err error
		baselineEntries, err = LoadBaseline(opts.BaselinePath)
		if err != nil {
			return nil, err
		}
	}
	res.BaselineEntries = baselineEntries

	newFindings, staleEntries := MatchFindings(allFindings, baselineEntries, allCheckedFiles, baselineRoot)
	res.NewFindings = newFindings
	res.StaleEntries = staleEntries

	return res, nil
}

func checkPackagesForGOOS(dir string, patterns []string, targetGOOS string, skipTests bool, skipGenerated bool, skipGlobs []string, matcher *ExclusionMatcher, apRoot, repoRoot string) ([]Finding, map[string]bool, error) {
	cfg := &packages.Config{
		Mode:  packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedTypes | packages.NeedTypesSizes | packages.NeedSyntax | packages.NeedTypesInfo,
		Tests: !skipTests,
		Dir:   dir,
	}
	if targetGOOS != "" {
		cfg.Env = append(os.Environ(), "GOOS="+targetGOOS)
	}

	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load packages in %s (GOOS=%s): %w", dir, targetGOOS, err)
	}

	policy := fileset.NewPolicy(skipGenerated, skipTests, skipGlobs, repoRoot)

	checkedFiles := make(map[string]bool)
	var findings []Finding

	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			return nil, nil, fmt.Errorf("package load/type error in %s: %s", pkg.PkgPath, pkg.Errors[0].Msg)
		}

		for _, file := range pkg.Syntax {
			pos := pkg.Fset.Position(file.Pos())
			if policy.ShouldSkip(pos.Filename, file) {
				continue
			}
			recordCheckedFile(pos.Filename, apRoot, repoRoot, dir, checkedFiles)

			rawList := findDroppedErrorsInFile(file, pkg.TypesInfo, matcher)
			for _, raw := range rawList {
				rawPos := pkg.Fset.Position(raw.pos)
				enclosingFunc := findEnclosingFunc(file, pkg.Fset, raw.pos)
				callee := formatCallee(raw.expr, pkg.TypesInfo)

				relFile := relPath(apRoot, rawPos.Filename)
				repoRel := relPath(repoRoot, rawPos.Filename)
				modRel := relPath(dir, rawPos.Filename)

				findings = append(findings, Finding{
					Pos:           rawPos,
					Category:      raw.category,
					Callee:        callee,
					EnclosingFunc: enclosingFunc,
					RelFile:       relFile,
					RepoRel:       repoRel,
					ModRel:        modRel,
				})
			}
		}
	}

	return findings, checkedFiles, nil
}

func recordCheckedFile(path, apRoot, repoRoot, dir string, checkedFiles map[string]bool) {
	checkedFiles[filepath.ToSlash(path)] = true
	if apRoot != "" {
		checkedFiles[relPath(apRoot, path)] = true
	}
	if repoRoot != "" {
		checkedFiles[relPath(repoRoot, path)] = true
	}
	if dir != "" {
		checkedFiles[relPath(dir, path)] = true
	}
}

func relPath(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return filepath.ToSlash(target)
	}
	return filepath.ToSlash(rel)
}

func findEnclosingFunc(file *ast.File, fset *token.FileSet, pos token.Pos) string {
	targetOffset := fset.Position(pos).Offset

	// Check function declarations first
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			start := fset.Position(fn.Pos()).Offset
			end := fset.Position(fn.End()).Offset
			if targetOffset >= start && targetOffset <= end {
				return formatEnclosingFunc(fn)
			}
		}
	}

	// Check package-level variable declarations (e.g. var x = func() { _ = f() })
	for _, decl := range file.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.VAR {
			for _, spec := range gen.Specs {
				if vspec, ok := spec.(*ast.ValueSpec); ok {
					start := fset.Position(vspec.Pos()).Offset
					end := fset.Position(vspec.End()).Offset
					if targetOffset >= start && targetOffset <= end {
						if len(vspec.Names) > 0 {
							return vspec.Names[0].Name
						}
					}
				}
			}
		}
	}

	return "<init>"
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
