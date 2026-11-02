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
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/baseline"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
	"k8s.io/klog/v2"
)

// Finding represents a portability finding in a task script.
type Finding struct {
	File        string
	Line        int
	Construct   string
	Alternative string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: %s (%s)", f.File, f.Line, f.Construct, f.Alternative)
}

// ToBaselineFinding converts a Finding into a baseline.Finding.
func (f Finding) ToBaselineFinding(baselineRoot, repoRoot string) baseline.Finding {
	relFile := relPath(baselineRoot, f.File)
	return baseline.Finding{
		Pos: token.Position{
			Filename: f.File,
			Line:     f.Line,
		},
		Detail:        f.Construct,
		EnclosingFunc: "-",
		RelFile:       relFile,
		RepoRel:       relPath(repoRoot, f.File),
		Message:       fmt.Sprintf("%s (%s)", f.Construct, f.Alternative),
	}
}

// portabilityFindings checks a task script line by line for non-portable constructs.
// Lines inside heredocs are evaluated line-by-line like any other line to keep matching simple.
func portabilityFindings(path string, lines []string) []Finding {
	var findings []Finding

	for lineNum, rawLine := range lines {
		line := rawLine
		if idx := strings.Index(line, "#"); idx != -1 {
			line = line[:idx]
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		segments := splitCommandSegments(line)

		for _, seg := range segments {
			fields := strings.Fields(seg)
			if len(fields) == 0 {
				continue
			}

			// Skip leading shell keywords or prefix wrappers
			cmdIdx := 0
			for cmdIdx < len(fields) {
				token := fields[cmdIdx]
				if token == "if" || token == "then" || token == "else" || token == "elif" ||
					token == "time" || token == "sudo" || token == "exec" || token == "do" {
					cmdIdx++
					continue
				}
				break
			}
			if cmdIdx >= len(fields) {
				continue
			}

			cmd := filepath.Base(fields[cmdIdx])
			args := fields[cmdIdx+1:]

			// 1. sed -i not followed by an explicit suffix argument
			if cmd == "sed" {
				for _, arg := range args {
					if arg == "-i" {
						findings = append(findings, Finding{
							File:        path,
							Line:        lineNum + 1,
							Construct:   "sed -i",
							Alternative: "use sed -i.bak ... && rm or write to a temp file; GNU and BSD disagree on -i",
						})
						break
					}
				}
			}

			// 2. date -d (GNU only)
			if cmd == "date" {
				for _, arg := range args {
					if arg == "-d" || strings.HasPrefix(arg, "-d") {
						findings = append(findings, Finding{
							File:        path,
							Line:        lineNum + 1,
							Construct:   "date -d",
							Alternative: "GNU only; compute dates in Go or use date -u +%s arithmetic",
						})
						break
					}
				}
			}

			// 3. stat -c (GNU only)
			if cmd == "stat" {
				for _, arg := range args {
					if arg == "-c" || strings.HasPrefix(arg, "-c") {
						findings = append(findings, Finding{
							File:        path,
							Line:        lineNum + 1,
							Construct:   "stat -c",
							Alternative: "GNU only; use wc -c <file for sizes, or Go",
						})
						break
					}
				}
			}

			// 4. grep -P (not portable)
			if cmd == "grep" {
				for _, arg := range args {
					if arg == "-P" || strings.HasPrefix(arg, "-P") {
						findings = append(findings, Finding{
							File:        path,
							Line:        lineNum + 1,
							Construct:   "grep -P",
							Alternative: "not portable; use grep -E",
						})
						break
					}
				}
			}

			// 5. find ... -printf (GNU only)
			if cmd == "find" {
				for _, arg := range args {
					if arg == "-printf" {
						findings = append(findings, Finding{
							File:        path,
							Line:        lineNum + 1,
							Construct:   "find ... -printf",
							Alternative: "GNU only",
						})
						break
					}
				}
			}

			// 6. xargs -r (GNU only)
			if cmd == "xargs" {
				for _, arg := range args {
					if arg == "-r" || arg == "--no-run-if-empty" {
						findings = append(findings, Finding{
							File:        path,
							Line:        lineNum + 1,
							Construct:   "xargs -r",
							Alternative: "GNU only; BSD xargs already does nothing on empty input",
						})
						break
					}
				}
			}

			// 7. timeout as a command (absent on macOS)
			if cmd == "timeout" {
				findings = append(findings, Finding{
					File:        path,
					Line:        lineNum + 1,
					Construct:   "timeout",
					Alternative: "absent on macOS; use go test -timeout or Go",
				})
			}

			// 8a. mapfile (bash 4+)
			if cmd == "mapfile" {
				findings = append(findings, Finding{
					File:        path,
					Line:        lineNum + 1,
					Construct:   "mapfile",
					Alternative: "bash 4+; macOS /bin/bash is 3.2",
				})
			}

			// 8b. readarray (bash 4+)
			if cmd == "readarray" {
				findings = append(findings, Finding{
					File:        path,
					Line:        lineNum + 1,
					Construct:   "readarray",
					Alternative: "bash 4+; macOS /bin/bash is 3.2",
				})
			}

			// 8c. declare -A (bash 4+)
			if cmd == "declare" {
				for _, arg := range args {
					if arg == "-A" || strings.HasPrefix(arg, "-A") {
						findings = append(findings, Finding{
							File:        path,
							Line:        lineNum + 1,
							Construct:   "declare -A",
							Alternative: "bash 4+; macOS /bin/bash is 3.2",
						})
						break
					}
				}
			}
		}

		// 8d & 8e. ${name,,} and ${name^^} (bash 4+)
		if strings.Contains(line, "${") {
			if strings.Contains(line, ",,") {
				findings = append(findings, Finding{
					File:        path,
					Line:        lineNum + 1,
					Construct:   "${name,,}",
					Alternative: "bash 4+; macOS /bin/bash is 3.2",
				})
			}
			if strings.Contains(line, "^^") {
				findings = append(findings, Finding{
					File:        path,
					Line:        lineNum + 1,
					Construct:   "${name^^}",
					Alternative: "bash 4+; macOS /bin/bash is 3.2",
				})
			}
		}
	}

	return findings
}

// splitCommandSegments splits a shell line into command segments taking quotes into account.
func splitCommandSegments(s string) []string {
	var segments []string
	var current strings.Builder
	inSingle := false
	inDouble := false

	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\'' && !inDouble {
			inSingle = !inSingle
			current.WriteByte(c)
			continue
		}
		if c == '"' && !inSingle {
			inDouble = !inDouble
			current.WriteByte(c)
			continue
		}

		if !inSingle && !inDouble {
			if c == ';' || c == '|' || c == '&' || c == '(' || c == ')' || c == '`' {
				seg := strings.TrimSpace(current.String())
				if seg != "" {
					segments = append(segments, seg)
				}
				current.Reset()
				continue
			}
		}
		current.WriteByte(c)
	}

	if seg := strings.TrimSpace(current.String()); seg != "" {
		segments = append(segments, seg)
	}
	return segments
}

// PortabilityTask checks task scripts in dev/tasks for non-portable constructs.
type PortabilityTask struct {
	APRoot        string
	RepoRoot      string
	SourceTree    *sourcetree.SourceTree
	Mode          codestyle.Mode
	BaselinePath  string
	WriteBaseline bool
}

// hasTaskScripts reports whether the AP root contains any task scripts under dev/tasks.
func hasTaskScripts(r *sourcetree.SourceTree, apRoot string) bool {
	if r == nil {
		return false
	}
	view := r.Under(apRoot)
	for _, f := range view.Files() {
		rel, err := filepath.Rel(apRoot, f.Path)
		if err != nil {
			continue
		}
		slash := filepath.ToSlash(rel)
		if strings.HasPrefix(slash, "dev/tasks/") || strings.Contains(slash, "/dev/tasks/") {
			return true
		}
	}
	return false
}

// NewPortabilityTask creates a new PortabilityTask for the given AP root if it contains task scripts and is enabled.
func NewPortabilityTask(r *sourcetree.SourceTree, apRoot, repoRoot string) (tasks.Task, error) {
	if !hasTaskScripts(r, apRoot) {
		return nil, nil
	}

	cfg, err := config.Load(apRoot)
	if err != nil {
		return nil, err
	}

	reg := codestyle.Find("portability")
	mode := codestyle.ModeError
	if reg != nil {
		mode = reg.ResolveMode(cfg)
	}
	if mode == codestyle.ModeIgnore {
		return nil, nil
	}

	configuredBaseline := ""
	if lc := cfg.Linter("portability"); lc != nil {
		configuredBaseline = lc.GetBaseline()
	}

	return &PortabilityTask{
		APRoot:       apRoot,
		RepoRoot:     repoRoot,
		SourceTree:   r,
		Mode:         mode,
		BaselinePath: configuredBaseline,
	}, nil
}

func (t *PortabilityTask) GetName() string {
	return "portability"
}

func (t *PortabilityTask) GetChildren() []tasks.Task {
	return nil
}

func (t *PortabilityTask) findTaskScripts(scope *tasks.APScope) []string {
	st := t.SourceTree
	if st == nil && scope != nil {
		st = scope.SourceTree
	}
	if st == nil {
		return nil
	}

	var scriptFiles []string
	view := st.Under(t.APRoot)
	for _, f := range view.Files() {
		rel, err := filepath.Rel(t.APRoot, f.Path)
		if err != nil {
			continue
		}
		slash := filepath.ToSlash(rel)
		if strings.HasPrefix(slash, "dev/tasks/") || strings.Contains(slash, "/dev/tasks/") {
			scriptFiles = append(scriptFiles, f.Path)
		}
	}
	sort.Strings(scriptFiles)
	return scriptFiles
}

func (t *PortabilityTask) Run(ctx context.Context, scope *tasks.APScope) error {
	scriptFiles := t.findTaskScripts(scope)
	if len(scriptFiles) == 0 {
		return nil
	}

	allCheckedFiles := make(map[string]bool)
	var allFindings []baseline.Finding
	for _, path := range scriptFiles {
		allCheckedFiles[filepath.ToSlash(path)] = true
		if t.RepoRoot != "" {
			allCheckedFiles[relPath(t.RepoRoot, path)] = true
		}
		if t.APRoot != "" {
			allCheckedFiles[relPath(t.APRoot, path)] = true
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read task script %s: %w", path, err)
		}
		lines := strings.Split(string(data), "\n")
		findings := portabilityFindings(path, lines)
		for _, f := range findings {
			allFindings = append(allFindings, f.ToBaselineFinding(t.APRoot, t.RepoRoot))
		}
	}

	baselineRoot := t.APRoot
	if baselineRoot == "" {
		baselineRoot = t.RepoRoot
	}
	baselinePath := baseline.ResolveBaselinePath(t.RepoRoot, t.APRoot, "portability", t.BaselinePath, t.WriteBaseline)
	if baselinePath != "" {
		bDir := filepath.Dir(baselinePath)
		if filepath.Base(bDir) == ".ap" {
			baselineRoot = filepath.Dir(bDir)
		}
	}

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
		return allFindings[i].Detail < allFindings[j].Detail
	})

	if t.WriteBaseline {
		var existingEntries []baseline.Entry
		if baselinePath != "" {
			entries, err := baseline.Load(baselinePath)
			if err != nil {
				return err
			}
			existingEntries = entries
		}

		hasChanges := false
		if len(allFindings) > 0 {
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

		if hasChanges {
			for _, f := range allFindings {
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
				if err := baseline.Write(baselinePath, "portability", finalEntries); err != nil {
					return fmt.Errorf("failed to write baseline to %s: %w", baselinePath, err)
				}
			}
			if _, err := fmt.Fprintf(scope.GetStdout(), "Wrote %d baseline entries to %s\n", len(finalEntries), baselinePath); err != nil {
				klog.V(2).Infof("failed to write baseline message: %v", err)
			}
		}
		return nil
	}

	var baselineEntries []baseline.Entry
	if baselinePath != "" {
		entries, err := baseline.Load(baselinePath)
		if err != nil {
			return err
		}
		baselineEntries = entries
	}

	newFindings, staleEntries := baseline.Match(allFindings, baselineEntries, allCheckedFiles, baselineRoot)
	for _, stale := range staleEntries {
		if _, err := fmt.Fprintf(scope.GetStdout(), "stale baseline entry: %s (remove from baseline)\n", stale); err != nil {
			klog.V(2).Infof("failed to write stale entry: %v", err)
		}
	}

	findingsToReport := newFindings
	if len(baselineEntries) == 0 {
		findingsToReport = allFindings
	}

	for _, f := range findingsToReport {
		msg := f.Message
		if msg == "" {
			msg = f.Detail
		}
		if _, err := fmt.Fprintf(scope.GetStderr(), "%s: %s\n", f.Pos, msg); err != nil {
			klog.V(2).Infof("failed to write diagnostic: %v", err)
		}
	}

	mode := t.Mode
	if mode == "" {
		mode = codestyle.ModeError
	}
	if len(findingsToReport) > 0 {
		if mode == codestyle.ModeError {
			return fmt.Errorf("portability check failed in %s", t.APRoot)
		} else if mode == codestyle.ModeWarn {
			klog.Warningf("portability check failed in %s: %d diagnostics found", t.APRoot, len(findingsToReport))
		}
	}
	return nil
}
