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

package testonlyimports

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/fileset"
	"golang.org/x/tools/go/analysis"
)

// DefaultPackages is the default list of test-only package import prefixes.
var DefaultPackages = []string{
	"github.com/google/go-cmp",
	"github.com/stretchr/testify",
	"golang.org/x/tools/go/analysis/analysistest",
	"k8s.io/client-go/kubernetes/fake",
}

// PackageListFlag implements flag.Value for repeated or comma-separated test-only package flags.
// Setting an empty value resets the list.
type PackageListFlag []string

func (p *PackageListFlag) String() string {
	return strings.Join(*p, ",")
}

func (p *PackageListFlag) Set(val string) error {
	if val == "" {
		*p = nil
		return nil
	}
	for _, part := range strings.Split(val, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			*p = append(*p, part)
		}
	}
	return nil
}

var (
	packagesFlag PackageListFlag
	policyFlags  *fileset.PolicyFlags
)

// Analyzer checks for imports of test-only packages from non-test Go code.
var Analyzer = &analysis.Analyzer{
	Name: "testonlyimports",
	Doc:  "check for imports of test-only packages from non-test Go code",
	Run:  run,
}

func init() {
	Analyzer.Flags.Var(&packagesFlag, "package", "test-only package prefix (can be repeated or comma-separated; replaces default list)")
	Analyzer.Flags.Var(&packagesFlag, "packages", "test-only package prefix (can be repeated or comma-separated; replaces default list)")
	policyFlags = fileset.RegisterFlags(Analyzer, false, false)
}

func run(pass *analysis.Pass) (any, error) {
	policy := policyFlags.NewPolicy()

	packages := DefaultPackages
	if len(packagesFlag) > 0 {
		packages = packagesFlag
	}

	for _, f := range pass.Files {
		if policy.ShouldSkipAST(pass.Fset, f) {
			continue
		}

		filename := pass.Fset.Position(f.Pos()).Filename
		if isTestFile(filename) {
			continue
		}

		for _, imp := range f.Imports {
			if imp.Path == nil {
				continue
			}
			pathVal, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				pathVal = strings.Trim(imp.Path.Value, `"`)
			}
			if isTestOnly(pathVal, packages) {
				pass.Reportf(imp.Pos(), "import of test-only package %s from non-test file", pathVal)
			}
		}
	}
	return nil, nil
}

// isTestFile reports whether filename is a test file (_test.go) or is under a testdata/ directory.
// Files under analysistest's harness root (testdata/src/<pkg>/...) are not exempt unless they are _test.go
// or under a nested testdata/ directory.
func isTestFile(filename string) bool {
	if fileset.IsTest(filename) {
		return true
	}
	slash := filepath.ToSlash(filepath.Clean(filename))
	parts := strings.Split(slash, "/")
	for i, part := range parts {
		if part == "testdata" {
			if i+1 < len(parts) && parts[i+1] == "src" {
				continue
			}
			return true
		}
	}
	return false
}

// isTestOnly reports whether importPath equals or has the prefix of any test-only package.
func isTestOnly(importPath string, testOnlyPackages []string) bool {
	for _, pkg := range testOnlyPackages {
		pkg = strings.TrimSuffix(pkg, "/")
		if importPath == pkg || strings.HasPrefix(importPath, pkg+"/") {
			return true
		}
	}
	return false
}
