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

package golang

import (
	"context"
	"errors"
	"fmt"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/baseline"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/closewriter"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/fileset"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/testonlyimports"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/walker"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/checker"
	"golang.org/x/tools/go/packages"
	"k8s.io/klog/v2"
)

// GoVetTask represents a task to run go vet.
type GoVetTask struct {
	Dir string
}

func (t *GoVetTask) Run(ctx context.Context, scope *tasks.APScope) error {
	klog.Infof("Running go vet in %s", t.Dir)
	vetCmd := exec.CommandContext(ctx, "go", "vet", "./...")
	vetCmd.Dir = t.Dir
	vetCmd.Stdout = scope.GetStdout()
	vetCmd.Stderr = scope.GetStderr()
	if err := vetCmd.Run(); err != nil {
		return fmt.Errorf("go vet failed in %s: %w", t.Dir, err)
	}
	return nil
}

func (t *GoVetTask) GetName() string {
	return "go-vet"
}

func (t *GoVetTask) GetChildren() []tasks.Task {
	return nil
}

// GovulncheckTask represents a task to run govulncheck.
type GovulncheckTask struct {
	Dir string
}

func (t *GovulncheckTask) Run(ctx context.Context, scope *tasks.APScope) error {
	klog.Infof("Running govulncheck in %s", t.Dir)
	vulnCmd := exec.CommandContext(ctx, "go", "run", "golang.org/x/vuln/cmd/govulncheck@latest", "./...")
	vulnCmd.Dir = t.Dir
	vulnCmd.Stdout = scope.GetStdout()
	vulnCmd.Stderr = scope.GetStderr()
	if err := vulnCmd.Run(); err != nil {
		return fmt.Errorf("govulncheck failed in %s: %w", t.Dir, err)
	}
	return nil
}

func (t *GovulncheckTask) GetName() string {
	return "govulncheck"
}

func (t *GovulncheckTask) GetChildren() []tasks.Task {
	return nil
}

// AnalyzersTask represents a task to run enabled code style analyzers in-process.
type AnalyzersTask struct {
	Dir           string
	RepoRoot      string
	Analyzers     []*analysis.Analyzer
	Enabled       map[string]codestyle.Mode
	Policies      map[string]fileset.Policy
	Settings      map[string]config.LinterConfig
	GOOS          []string
	HostGOOS      string
	LoadCount     int
	WriteBaseline bool
	BaselinePath  string
}

// SkipGenerated reports whether skipGenerated is enabled for the named analyzer.
func (t *AnalyzersTask) SkipGenerated(name string) bool {
	if p, ok := t.Policies[name]; ok {
		return p.SkipGenerated
	}
	return false
}

// SkipTests reports whether skipTests is enabled for the named analyzer.
func (t *AnalyzersTask) SkipTests(name string) bool {
	if p, ok := t.Policies[name]; ok {
		return p.SkipTests
	}
	return false
}

// Packages returns any packages configured for the named analyzer.
func (t *AnalyzersTask) Packages(name string) []string {
	if s, ok := t.Settings[name].(*testonlyimports.Settings); ok && s != nil {
		return s.Packages
	}
	return nil
}

// Types returns any types configured for the named analyzer.
func (t *AnalyzersTask) Types(name string) []string {
	if s, ok := t.Settings[name].(*closewriter.Settings); ok && s != nil {
		return s.Types
	}
	return nil
}

func (t *AnalyzersTask) Run(ctx context.Context, scope *tasks.APScope) error {
	klog.Infof("Running analyzers in %s", t.Dir)

	if len(t.Analyzers) == 0 {
		return nil
	}

	targetGOOSList := t.GOOS
	if len(targetGOOSList) == 0 {
		host := t.HostGOOS
		if host == "" {
			host = runtime.GOOS
		}
		targetGOOSList = []string{host}
	}

	type diagKey struct {
		pos     token.Position
		end     token.Position
		name    string
		message string
	}
	seen := make(map[diagKey]bool)
	seenFindings := make(map[string]bool)
	findingsByAnalyzer := make(map[string][]baseline.Finding)

	allCheckedFiles := make(map[string]bool)
	var actionErrors []error

	for _, targetGOOS := range targetGOOSList {
		cfg := &packages.Config{
			Context: ctx,
			Mode:    packages.LoadAllSyntax | packages.NeedModule,
			Dir:     t.Dir,
			Tests:   true,
		}
		if targetGOOS != runtime.GOOS {
			cfg.Env = append(os.Environ(), "GOOS="+targetGOOS)
		}

		t.LoadCount++
		pkgs, err := packages.Load(cfg, "./...")
		if err != nil {
			return fmt.Errorf("failed to load packages in %s: %w", t.Dir, err)
		}
		if len(pkgs) == 0 {
			continue
		}

		var pkgErrors int
		for _, pkg := range pkgs {
			for _, err := range pkg.Errors {
				if _, writeErr := fmt.Fprintln(scope.GetStderr(), err); writeErr != nil {
					klog.V(2).Infof("failed to write package error: %v", writeErr)
				}
				pkgErrors++
			}
		}
		if pkgErrors > 0 {
			return fmt.Errorf("failed to load packages in %s: %d error(s)", t.Dir, pkgErrors)
		}

		for _, pkg := range pkgs {
			for _, file := range pkg.Syntax {
				pos := pkg.Fset.Position(file.Pos())
				allCheckedFiles[filepath.ToSlash(pos.Filename)] = true
				if t.RepoRoot != "" {
					allCheckedFiles[relPath(t.RepoRoot, pos.Filename)] = true
				}
				if t.Dir != "" {
					allCheckedFiles[relPath(t.Dir, pos.Filename)] = true
				}
			}
		}

		graph, err := checker.Analyze(t.Analyzers, pkgs, nil)
		if err != nil {
			return fmt.Errorf("analyzers failed in %s: %w", t.Dir, err)
		}

		for _, act := range graph.Roots {
			if act.Err != nil {
				actionErrors = append(actionErrors, fmt.Errorf("%s: %w", act.Analyzer.Name, act.Err))
				continue
			}
			if !act.IsRoot {
				continue
			}
			if fList, ok := act.Result.([]baseline.Finding); ok {
				for _, f := range fList {
					k := f.Key()
					if seenFindings[k] {
						continue
					}
					seenFindings[k] = true
					findingsByAnalyzer[act.Analyzer.Name] = append(findingsByAnalyzer[act.Analyzer.Name], f)
				}
				continue
			}
			for _, diag := range act.Diagnostics {
				posn := act.Package.Fset.Position(diag.Pos)
				end := act.Package.Fset.Position(diag.End)
				k := diagKey{pos: posn, end: end, name: act.Analyzer.Name, message: diag.Message}
				if seen[k] {
					continue
				}
				seen[k] = true

				var enclosingFunc string
				for _, file := range act.Package.Syntax {
					if act.Package.Fset.Position(file.Pos()).Filename == posn.Filename {
						enclosingFunc = baseline.FindEnclosingFunc(file, act.Package.Fset, diag.Pos)
						break
					}
				}
				if enclosingFunc == "" {
					enclosingFunc = "<init>"
				}

				var related []baseline.RelatedInfo
				for _, rel := range diag.Related {
					related = append(related, baseline.RelatedInfo{
						Pos:     act.Package.Fset.Position(rel.Pos),
						Message: rel.Message,
					})
				}

				findingsByAnalyzer[act.Analyzer.Name] = append(findingsByAnalyzer[act.Analyzer.Name], baseline.Finding{
					Pos:           posn,
					EnclosingFunc: enclosingFunc,
					Detail:        diag.Message,
					Message:       diag.Message,
					Related:       related,
				})
			}
		}
	}

	var errs []error
	for _, a := range t.Analyzers {
		findings := findingsByAnalyzer[a.Name]

		baselineRoot := t.RepoRoot
		if baselineRoot == "" {
			baselineRoot = t.Dir
		}

		configured := ""
		if lc := t.Settings[a.Name]; lc != nil {
			configured = lc.GetBaseline()
		}
		if configured == "" && (len(t.Analyzers) == 1 || a.Name == "droppederrors") {
			configured = t.BaselinePath
		}

		baselinePath := baseline.ResolveBaselinePath(t.RepoRoot, t.Dir, a.Name, configured, t.WriteBaseline)
		if baselinePath != "" {
			bDir := filepath.Dir(baselinePath)
			if filepath.Base(bDir) == ".ap" {
				baselineRoot = filepath.Dir(bDir)
			}
		}

		for i := range findings {
			findings[i].RelFile = relPath(baselineRoot, findings[i].Pos.Filename)
			findings[i].RepoRel = relPath(t.RepoRoot, findings[i].Pos.Filename)
			findings[i].ModRel = relPath(t.Dir, findings[i].Pos.Filename)
		}

		sort.Slice(findings, func(i, j int) bool {
			if findings[i].RelFile != findings[j].RelFile {
				return findings[i].RelFile < findings[j].RelFile
			}
			if findings[i].Pos.Line != findings[j].Pos.Line {
				return findings[i].Pos.Line < findings[j].Pos.Line
			}
			if findings[i].Pos.Column != findings[j].Pos.Column {
				return findings[i].Pos.Column < findings[j].Pos.Column
			}
			if findings[i].Category != findings[j].Category {
				return findings[i].Category < findings[j].Category
			}
			return findings[i].Detail < findings[j].Detail
		})

		if t.WriteBaseline {
			var existingEntries []baseline.Entry
			if baselinePath != "" {
				entries, err := baseline.Load(baselinePath)
				if err != nil {
					errs = append(errs, err)
					continue
				}
				existingEntries = entries
			}

			hasChanges := false
			if len(findings) > 0 {
				hasChanges = true
			}
			var finalEntries []baseline.Entry
			for _, e := range existingEntries {
				entryFile := filepath.ToSlash(e.File)
				if !allCheckedFiles[entryFile] {
					finalEntries = append(finalEntries, e)
				} else {
					hasChanges = true
				}
			}

			if !hasChanges {
				continue
			}

			for _, f := range findings {
				relFile := relPath(baselineRoot, f.Pos.Filename)
				finalEntries = append(finalEntries, baseline.Entry{
					File:          relFile,
					EnclosingFunc: f.EnclosingFunc,
					Category:      f.Category,
					Detail:        f.Detail,
					Callee:        f.Detail,
				})
			}

			if baselinePath != "" {
				if err := baseline.Write(baselinePath, a.Name, finalEntries); err != nil {
					errs = append(errs, fmt.Errorf("failed to write baseline to %s: %w", baselinePath, err))
					continue
				}
			}
			if _, err := fmt.Fprintf(scope.GetStdout(), "Wrote %d baseline entries to %s\n", len(finalEntries), baselinePath); err != nil {
				klog.V(2).Infof("failed to write baseline message: %v", err)
			}
			continue
		}

		var baselineEntries []baseline.Entry
		if baselinePath != "" {
			entries, err := baseline.Load(baselinePath)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			baselineEntries = entries
		}

		newFindings, staleEntries := baseline.Match(findings, baselineEntries, allCheckedFiles, baselineRoot)
		for _, stale := range staleEntries {
			if _, err := fmt.Fprintf(scope.GetStdout(), "stale baseline entry: %s (remove from baseline)\n", stale); err != nil {
				klog.V(2).Infof("failed to write stale entry: %v", err)
			}
		}

		findingsToReport := newFindings
		if len(baselineEntries) == 0 {
			findingsToReport = findings
		}

		for _, f := range findingsToReport {
			msg := f.Message
			if msg == "" {
				msg = f.Detail
			}
			if _, err := fmt.Fprintf(scope.GetStderr(), "%s: %s\n", f.Pos, msg); err != nil {
				klog.V(2).Infof("failed to write diagnostic: %v", err)
			}
			for _, rel := range f.Related {
				if _, err := fmt.Fprintf(scope.GetStderr(), "%s:\t%s\n", rel.Pos, rel.Message); err != nil {
					klog.V(2).Infof("failed to write related info: %v", err)
				}
			}
		}

		mode := t.Enabled[a.Name]
		if len(findingsToReport) > 0 {
			if mode == codestyle.ModeError {
				errs = append(errs, fmt.Errorf("%s check failed in %s", a.Name, t.Dir))
			} else if mode == codestyle.ModeWarn {
				klog.Warningf("%s check failed in %s: %d diagnostics found", a.Name, t.Dir, len(findingsToReport))
			}
		}
	}

	errs = append(errs, actionErrors...)
	return errors.Join(errs...)
}

// SetWriteBaseline recursively sets WriteBaseline on any AnalyzersTask or PortabilityTask in the task tree.
func SetWriteBaseline(t tasks.Task, writeBaseline bool) {
	if at, ok := t.(*AnalyzersTask); ok {
		at.WriteBaseline = writeBaseline
	}
	if pt, ok := t.(*PortabilityTask); ok {
		pt.WriteBaseline = writeBaseline
	}
	for _, child := range t.GetChildren() {
		SetWriteBaseline(child, writeBaseline)
	}
}

func relPath(base, target string) string {
	if base == "" {
		return filepath.ToSlash(target)
	}
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return filepath.ToSlash(target)
	}
	return filepath.ToSlash(rel)
}

func (t *AnalyzersTask) GetName() string {
	return "analyzers"
}

func (t *AnalyzersTask) GetChildren() []tasks.Task {
	return nil
}

func determineTargetGOOS(r *sourcetree.SourceTree, dir string, skipGlobs []string, hostGOOS string) []string {
	if hostGOOS == "" {
		hostGOOS = runtime.GOOS
	}
	referenced := make(map[string]bool)

	var ignoreList *walker.IgnoreList
	if len(skipGlobs) > 0 {
		ignoreList = walker.NewIgnoreList(skipGlobs)
	}

	view := r.Under(dir)
	for _, f := range view.Files() {
		if !strings.HasSuffix(f.Name, ".go") {
			continue
		}
		if f.Module() != dir && f.Module() != "" {
			continue
		}
		if fileset.IsVendored(f.RelPath) || fileset.IsVendored(f.Path) {
			continue
		}
		if ignoreList != nil && ignoreList.ShouldIgnore(f.RelPath, false) {
			continue
		}
		for _, goos := range f.ReferencedGOOS() {
			if goos != hostGOOS {
				referenced[goos] = true
			}
		}
	}

	res := []string{hostGOOS}
	if len(referenced) > 0 {
		var nonHost []string
		for osTag := range referenced {
			nonHost = append(nonHost, osTag)
		}
		sort.Strings(nonHost)
		res = append(res, nonHost...)
	}
	return res
}

// LintTasks returns a task group for running go linting in discovered modules.
func LintTasks(r *sourcetree.SourceTree, root string) (tasks.Task, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}

	modules := r.Under(root).Modules()

	var moduleTasks []tasks.Task
	for _, dir := range modules {
		hasGo, err := hasGoFiles(r, dir)
		if err != nil {
			return nil, fmt.Errorf("failed to check for Go files in %s: %w", dir, err)
		}
		if !hasGo {
			continue
		}

		modGroup := &tasks.Group{
			Name:            fmt.Sprintf("go-lint-%s", filepath.Base(dir)),
			ContinueOnError: true,
		}

		if cfg.IsGovetEnabled() {
			modGroup.Tasks = append(modGroup.Tasks, &GoVetTask{Dir: dir})
		}
		if cfg.IsGovulncheckEnabled() {
			modGroup.Tasks = append(modGroup.Tasks, &GovulncheckTask{Dir: dir})
		}

		analyzersTask := &AnalyzersTask{
			Dir:      dir,
			RepoRoot: root,
			Enabled:  make(map[string]codestyle.Mode),
			Policies: make(map[string]fileset.Policy),
			Settings: make(map[string]config.LinterConfig),
		}

		if explicitGOOS := cfg.LintGOOS(); len(explicitGOOS) > 0 {
			analyzersTask.GOOS = explicitGOOS
		} else {
			analyzersTask.GOOS = determineTargetGOOS(r, dir, cfg.Skip, runtime.GOOS)
		}

		for _, reg := range codestyle.Analyzers {
			mode := reg.ResolveMode(cfg)
			if mode == codestyle.ModeIgnore {
				continue
			}
			analyzersTask.Enabled[reg.Name] = mode

			skipGen := reg.ResolveSkipGenerated(cfg)
			skipTests := reg.ResolveSkipTests(cfg)
			policy := fileset.NewPolicy(skipGen, skipTests, cfg.Skip, root)
			analyzersTask.Policies[reg.Name] = policy
			analyzersTask.Settings[reg.Name] = cfg.Linter(reg.ConfigKey())

			if reg.New != nil {
				analyzer := reg.New(policy, cfg)
				analyzersTask.Analyzers = append(analyzersTask.Analyzers, analyzer)
			}
		}

		if len(analyzersTask.Analyzers) > 0 {
			modGroup.Tasks = append(modGroup.Tasks, analyzersTask)
		}

		if len(modGroup.Tasks) > 0 {
			moduleTasks = append(moduleTasks, modGroup)
		}
	}

	return &tasks.Group{
		Name:            "go-lints",
		Tasks:           moduleTasks,
		ContinueOnError: true,
		Parallel:        true,
	}, nil
}

// Lint runs go vet and govulncheck in discovered modules.
func Lint(ctx context.Context, r *sourcetree.SourceTree, root string) error {
	task, err := LintTasks(r, root)
	if err != nil {
		return err
	}
	return tasks.Run(ctx, tasks.NewScope(root, root, r), []tasks.Task{task}, tasks.RunOptions{})
}

// hasGoFiles returns true if the directory or any of its subdirectories
// (excluding those that are themselves Go modules) contain at least one .go file.
func hasGoFiles(r *sourcetree.SourceTree, root string) (bool, error) {
	return r.Under(root).HasGoFiles(), nil
}
