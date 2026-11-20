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

package fileset

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/walker"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
)

// IsGenerated reports whether the AST file represents generated code per https://go.dev/s/generatedcode.
func IsGenerated(file *ast.File) bool {
	if file == nil {
		return false
	}
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if isGen, _ := sourcetree.IsGeneratedLine(comment.Text); isGen {
				return true
			}
		}
	}
	return false
}

// IsTest reports whether the given filename is a Go test file.
func IsTest(filename string) bool {
	return strings.HasSuffix(filename, "_test.go")
}

// IsVendored reports whether the given relative path contains a "vendor" path segment.
func IsVendored(relPath string) bool {
	slash := filepath.ToSlash(filepath.Clean(relPath))
	for _, seg := range strings.Split(slash, "/") {
		if seg == "vendor" {
			return true
		}
	}
	return false
}

// Policy encapsulates file-filtering decisions for linters.
type Policy struct {
	SkipGenerated bool
	SkipTests     bool
	SkipVendored  bool
	SkipGlobs     []string
	RepoRoot      string

	ignoreList *walker.IgnoreList
}

// NewPolicy creates a Policy with eagerly compiled ignoreList to avoid data races.
func NewPolicy(skipGenerated bool, skipTests bool, skipGlobs []string, repoRoot string) (Policy, error) {
	p := Policy{
		SkipGenerated: skipGenerated,
		SkipTests:     skipTests,
		SkipVendored:  true,
		SkipGlobs:     skipGlobs,
		RepoRoot:      repoRoot,
	}
	if len(skipGlobs) > 0 {
		ignoreList, err := walker.NewIgnoreList(skipGlobs)
		if err != nil {
			return Policy{}, fmt.Errorf("compiling skip globs: %w", err)
		}
		p.ignoreList = ignoreList
	}
	return p, nil
}

func (p Policy) isVendored(filename string) bool {
	relPath := filename
	if p.RepoRoot != "" {
		if rel, err := filepath.Rel(p.RepoRoot, filename); err == nil && !strings.HasPrefix(rel, "..") {
			relPath = rel
		}
	}
	return IsVendored(relPath)
}

// ShouldSkip reports whether a file should be skipped.
func (p Policy) ShouldSkip(filename string, file *ast.File) bool {
	if p.SkipTests && IsTest(filename) {
		return true
	}
	if p.SkipVendored && p.isVendored(filename) {
		return true
	}
	if p.SkipGenerated && IsGenerated(file) {
		return true
	}
	if p.ignoreList != nil && filename != "" {
		relPath := filename
		if p.RepoRoot != "" {
			if rel, err := filepath.Rel(p.RepoRoot, filename); err == nil && !strings.HasPrefix(rel, "..") {
				relPath = rel
			}
		}
		if p.ignoreList.ShouldIgnore(relPath, false) {
			return true
		}
	}
	return false
}

// ShouldSkipAST reports whether an AST file in an analysis pass should be skipped.
func (p Policy) ShouldSkipAST(fset *token.FileSet, file *ast.File) bool {
	if file == nil {
		return false
	}
	var filename string
	if fset != nil {
		filename = fset.Position(file.Pos()).Filename
	}
	return p.ShouldSkip(filename, file)
}
