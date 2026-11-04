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

package portability

import (
	"context"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/baseline"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/tasks"
)

// Finding represents a portability finding in a task script.
type Finding struct {
	File        string
	Line        int
	Construct   string
	Alternative string
}

type rule struct {
	cmd       string
	flag      string
	construct string
	alt       string
}

var rules = []rule{
	{cmd: "sed", flag: "-i", construct: "sed -i", alt: "use sed -i.bak ... && rm or write to a temp file; GNU and BSD disagree on -i"},
	{cmd: "date", flag: "-d", construct: "date -d", alt: "GNU only; compute dates in Go or use date -u +%s arithmetic"},
	{cmd: "stat", flag: "-c", construct: "stat -c", alt: "GNU only; use wc -c <file for sizes, or Go"},
	{cmd: "grep", flag: "-P", construct: "grep -P", alt: "not portable; use grep -E"},
	{cmd: "find", flag: "-printf", construct: "find ... -printf", alt: "GNU only"},
	{cmd: "xargs", flag: "-r", construct: "xargs -r", alt: "GNU only; BSD xargs already does nothing on empty input"},
	{cmd: "timeout", construct: "timeout", alt: "absent on macOS; use go test -timeout or Go"},
	{cmd: "mapfile", construct: "mapfile", alt: "bash 4+; macOS /bin/bash is 3.2"},
	{cmd: "readarray", construct: "readarray", alt: "bash 4+; macOS /bin/bash is 3.2"},
	{cmd: "declare", flag: "-A", construct: "declare -A", alt: "bash 4+; macOS /bin/bash is 3.2"},
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
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}

		for _, r := range rules {
			if matchesRule(fields, r) {
				findings = append(findings, Finding{
					File:        path,
					Line:        lineNum + 1,
					Construct:   r.construct,
					Alternative: r.alt,
				})
			}
		}

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

func matchesRule(fields []string, r rule) bool {
	for i, f := range fields {
		cmd := normalizeCmd(f)
		if cmd == r.cmd || filepath.Base(cmd) == r.cmd {
			if r.flag == "" {
				return true
			}
			for _, arg := range fields[i+1:] {
				if arg == r.flag {
					return true
				}
			}
		}
	}
	return false
}

func normalizeCmd(token string) string {
	if idx := strings.LastIndex(token, "="); idx != -1 {
		token = token[idx+1:]
	}
	for {
		if strings.HasPrefix(token, "$(") {
			token = strings.TrimPrefix(token, "$(")
			continue
		}
		if strings.HasPrefix(token, "(") {
			token = strings.TrimPrefix(token, "(")
			continue
		}
		if strings.HasPrefix(token, "`") {
			token = strings.TrimPrefix(token, "`")
			continue
		}
		break
	}
	return token
}

// Task checks task scripts in dev/tasks for non-portable constructs.
type Task struct {
	Dir           string
	RepoRoot      string
	Scripts       []string
	Mode          codestyle.Mode
	BaselinePath  string
	WriteBaseline bool
}

func (t *Task) GetName() string {
	return "portability"
}

func (t *Task) GetChildren() []tasks.Task {
	return nil
}

func (t *Task) SetWriteBaseline(wb bool) {
	t.WriteBaseline = wb
}

// NewTask creates a new portability task for the given directory if task scripts exist.
func NewTask(tree *sourcetree.SourceTree, dir, repoRoot string) (tasks.Task, error) {
	scriptTasks, err := tasks.FindTaskScripts(tree, dir)
	if err != nil {
		return nil, err
	}
	if len(scriptTasks) == 0 {
		return nil, nil
	}

	cfg, err := config.Effective(tree, dir)
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

	var paths []string
	for _, st := range scriptTasks {
		if ts, ok := st.(*tasks.TaskScript); ok {
			paths = append(paths, ts.Path)
		}
	}

	return &Task{
		Dir:          dir,
		RepoRoot:     repoRoot,
		Scripts:      paths,
		Mode:         mode,
		BaselinePath: configuredBaseline,
	}, nil
}

func (t *Task) Run(ctx context.Context, scope *tasks.APScope) error {
	allCheckedFiles := make(map[string]bool)
	var allFindings []baseline.Finding

	for _, path := range t.Scripts {
		allCheckedFiles[filepath.ToSlash(path)] = true
		if t.RepoRoot != "" {
			rel, err := filepath.Rel(t.RepoRoot, path)
			if err != nil {
				return fmt.Errorf("failed to get relative path for %s: %w", path, err)
			}
			allCheckedFiles[filepath.ToSlash(rel)] = true
		}
		if t.Dir != "" {
			rel, err := filepath.Rel(t.Dir, path)
			if err != nil {
				return fmt.Errorf("failed to get relative path for %s: %w", path, err)
			}
			allCheckedFiles[filepath.ToSlash(rel)] = true
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read task script %s: %w", path, err)
		}
		lines := strings.Split(string(data), "\n")
		findings := portabilityFindings(path, lines)
		for _, f := range findings {
			allFindings = append(allFindings, baseline.Finding{
				Pos: token.Position{
					Filename: f.File,
					Line:     f.Line,
				},
				Detail:        f.Construct,
				EnclosingFunc: "-",
				Message:       fmt.Sprintf("%s (%s)", f.Construct, f.Alternative),
			})
		}
	}

	_, err := baseline.Apply(allFindings, baseline.ApplyOptions{
		Name:            "portability",
		RepoRoot:        t.RepoRoot,
		Dir:             t.Dir,
		ConfiguredPath:  t.BaselinePath,
		WriteBaseline:   t.WriteBaseline,
		AllCheckedFiles: allCheckedFiles,
		Mode:            string(t.Mode),
		Stdout:          scope.GetStdout(),
		Stderr:          scope.GetStderr(),
	})
	return err
}
