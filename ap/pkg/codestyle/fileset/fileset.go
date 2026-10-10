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
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/walker"
	"golang.org/x/tools/go/analysis"
)

// IsGenerated reports whether the AST file represents generated code per https://go.dev/s/generatedcode.
func IsGenerated(file *ast.File) bool {
	if file == nil {
		return false
	}
	return ast.IsGenerated(file)
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
func NewPolicy(skipGenerated bool, skipTests bool, skipGlobs []string, repoRoot string) *Policy {
	p := &Policy{
		SkipGenerated: skipGenerated,
		SkipTests:     skipTests,
		SkipVendored:  true,
		SkipGlobs:     skipGlobs,
		RepoRoot:      repoRoot,
	}
	if len(skipGlobs) > 0 {
		p.ignoreList = walker.NewIgnoreList(skipGlobs)
	}
	return p
}

func (p *Policy) isVendored(filename string) bool {
	relPath := filename
	if p.RepoRoot != "" {
		if rel, err := filepath.Rel(p.RepoRoot, filename); err == nil && !strings.HasPrefix(rel, "..") {
			relPath = rel
		}
	}
	return IsVendored(relPath)
}

// ShouldSkip reports whether a file should be skipped.
func (p *Policy) ShouldSkip(filename string, file *ast.File) bool {
	if p == nil {
		return false
	}
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
func (p *Policy) ShouldSkipAST(fset *token.FileSet, file *ast.File) bool {
	if p == nil || file == nil {
		return false
	}
	var filename string
	if fset != nil {
		filename = fset.Position(file.Pos()).Filename
	}
	return p.ShouldSkip(filename, file)
}

// StringListFlag implements flag.Value for repeated or comma-separated string flags.
type StringListFlag []string

func (s *StringListFlag) String() string {
	return strings.Join(*s, ",")
}

func (s *StringListFlag) Set(val string) error {
	for _, part := range strings.Split(val, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			*s = append(*s, part)
		}
	}
	return nil
}

// PolicyFlags holds flag values registered for an analyzer.
type PolicyFlags struct {
	SkipGenerated bool
	SkipTests     bool
	SkipGlobs     StringListFlag
	RepoRoot      string
}

// NewPolicy constructs a Policy from the parsed PolicyFlags.
func (pf *PolicyFlags) NewPolicy() *Policy {
	return NewPolicy(pf.SkipGenerated, pf.SkipTests, pf.SkipGlobs, pf.RepoRoot)
}

// RegisterFlags registers the standard file filtering flags (-skip-generated, -skip-tests, -skip-glob, -repo-root) on an analyzer.
func RegisterFlags(analyzer *analysis.Analyzer, defaultSkipGenerated bool, defaultSkipTests bool) *PolicyFlags {
	pf := &PolicyFlags{
		SkipGenerated: defaultSkipGenerated,
		SkipTests:     defaultSkipTests,
	}
	analyzer.Flags.BoolVar(&pf.SkipGenerated, "skip-generated", defaultSkipGenerated, "skip generated files")
	analyzer.Flags.BoolVar(&pf.SkipTests, "skip-tests", defaultSkipTests, "skip _test.go files")
	analyzer.Flags.Var(&pf.SkipGlobs, "skip-glob", "glob pattern to skip (can be repeated or comma-separated)")
	analyzer.Flags.StringVar(&pf.RepoRoot, "repo-root", "", "repository root directory for relativizing skip globs")
	return pf
}
