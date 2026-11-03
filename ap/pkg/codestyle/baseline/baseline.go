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

package baseline

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"k8s.io/klog/v2"
)

const (
	CategoryDroppedError               = "dropped-error"
	CategoryErrorOnlyCheckedForSuccess = "error-only-checked-for-success"
	CategoryErrorDiscarded             = "error-discarded"
)

// Entry represents a single tolerated finding site in a baseline file.
type Entry struct {
	File          string
	EnclosingFunc string
	Category      string
	Detail        string
	Callee        string // Synced with Detail for compatibility
}

// String returns the canonical string representation of a baseline entry.
func (e Entry) String() string {
	detail := e.Detail
	if detail == "" {
		detail = e.Callee
	}
	if e.Category != "" && e.Category != CategoryDroppedError {
		return fmt.Sprintf("%s:%s:%s:%s", e.File, e.EnclosingFunc, e.Category, detail)
	}
	return fmt.Sprintf("%s:%s:%s", e.File, e.EnclosingFunc, detail)
}

// Finding represents a diagnostic or finding in source code.
type Finding struct {
	Pos           token.Position
	Category      string
	Detail        string
	Callee        string // Synced with Detail for compatibility
	EnclosingFunc string
	RelFile       string // Relative to AP root / baseline root
	RepoRel       string // Relative to repo root
	ModRel        string // Relative to module dir
	Message       string
	Related       []RelatedInfo
}

// RelatedInfo represents secondary location information for a finding.
type RelatedInfo struct {
	Pos     token.Position
	Message string
}

// Key returns a unique signature for deduplication.
func (f Finding) Key() string {
	detail := f.Detail
	if detail == "" {
		detail = f.Callee
	}
	return f.Pos.Filename + ":" + f.Pos.String() + ":" + f.Category + ":" + f.EnclosingFunc + ":" + detail
}

// Parse parses baseline entries from raw bytes.
// Comments (starting with '#') and empty lines are ignored.
// Entries may be formatted as:
// - "file:enclosing:detail"
// - "file:enclosing:category:detail"
// - or whitespace-separated equivalents.
func Parse(data []byte) ([]Entry, error) {
	var entries []Entry
	scanner := bufio.NewScanner(bytes.NewReader(data))
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) == 4 {
			entries = append(entries, Entry{
				File:          filepath.ToSlash(fields[0]),
				EnclosingFunc: fields[1],
				Category:      fields[2],
				Detail:        fields[3],
				Callee:        fields[3],
			})
			continue
		}
		if len(fields) == 3 {
			entries = append(entries, Entry{
				File:          filepath.ToSlash(fields[0]),
				EnclosingFunc: fields[1],
				Category:      "",
				Detail:        fields[2],
				Callee:        fields[2],
			})
			continue
		}

		parts := strings.Split(line, ":")
		if len(parts) == 4 {
			entries = append(entries, Entry{
				File:          filepath.ToSlash(parts[0]),
				EnclosingFunc: parts[1],
				Category:      parts[2],
				Detail:        parts[3],
				Callee:        parts[3],
			})
			continue
		}
		if len(parts) == 3 {
			entries = append(entries, Entry{
				File:          filepath.ToSlash(parts[0]),
				EnclosingFunc: parts[1],
				Category:      "",
				Detail:        parts[2],
				Callee:        parts[2],
			})
			continue
		}
		if len(parts) > 4 {
			if parts[2] == CategoryErrorOnlyCheckedForSuccess || parts[2] == CategoryDroppedError || parts[2] == CategoryErrorDiscarded {
				entries = append(entries, Entry{
					File:          filepath.ToSlash(parts[0]),
					EnclosingFunc: parts[1],
					Category:      parts[2],
					Detail:        strings.Join(parts[3:], ":"),
					Callee:        strings.Join(parts[3:], ":"),
				})
				continue
			}
			entries = append(entries, Entry{
				File:          filepath.ToSlash(parts[0]),
				EnclosingFunc: parts[1],
				Category:      "",
				Detail:        strings.Join(parts[2:], ":"),
				Callee:        strings.Join(parts[2:], ":"),
			})
			continue
		}

		return nil, fmt.Errorf("line %d: invalid baseline entry %q", lineNum, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading baseline: %w", err)
	}
	return entries, nil
}

// Format formats baseline entries into a sorted, human-readable string.
func Format(analyzer string, entries []Entry) string {
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].File != sorted[j].File {
			return sorted[i].File < sorted[j].File
		}
		if sorted[i].EnclosingFunc != sorted[j].EnclosingFunc {
			return sorted[i].EnclosingFunc < sorted[j].EnclosingFunc
		}
		if sorted[i].Category != sorted[j].Category {
			return sorted[i].Category < sorted[j].Category
		}
		detailI := sorted[i].Detail
		if detailI == "" {
			detailI = sorted[i].Callee
		}
		detailJ := sorted[j].Detail
		if detailJ == "" {
			detailJ = sorted[j].Callee
		}
		return detailI < detailJ
	})

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Baseline for ap %s ratchet.\n", analyzer))
	if analyzer == "droppederrors" {
		sb.WriteString("# Entries: <file>:<enclosing-function>[:<category>]:<callee>\n")
	} else {
		sb.WriteString("# Entries: <file>:<enclosing-function>[:<category>]:<detail>\n")
	}
	sb.WriteString(fmt.Sprintf("# Do not add entries manually; fix errors or run 'ap lint %s --write-baseline' if approved.\n", analyzer))
	for _, e := range sorted {
		sb.WriteString(e.String())
		sb.WriteByte('\n')
	}
	return sb.String()
}

// Load loads baseline entries from the given path.
// If the file does not exist, it returns an empty slice and nil error.
func Load(path string) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read baseline %s: %w", path, err)
	}
	return Parse(data)
}

// Write writes baseline entries to the given path, creating any missing parent directories.
func Write(path, analyzer string, entries []Entry) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}
	content := Format(analyzer, entries)
	return os.WriteFile(path, []byte(content), 0644)
}

// Match compares findings against baseline entries using multiset matching.
// Returns findings not in the baseline (newFindings) and baseline entries that no longer occur
// in checked files (staleEntries).
func Match(findings []Finding, baseline []Entry, checkedFiles map[string]bool, rootDir string) (newFindings []Finding, staleEntries []Entry) {
	usedBaseline := make([]bool, len(baseline))

	for _, f := range findings {
		matched := false
		for i, entry := range baseline {
			if usedBaseline[i] {
				continue
			}
			if matchesEntry(entry, f) {
				usedBaseline[i] = true
				matched = true
				break
			}
		}
		if !matched {
			newFindings = append(newFindings, f)
		}
	}

	for i, entry := range baseline {
		if usedBaseline[i] {
			continue
		}
		if isStaleCandidate(entry, checkedFiles, rootDir) {
			staleEntries = append(staleEntries, entry)
		}
	}

	return newFindings, staleEntries
}

func normalizeCategory(cat string) string {
	if cat == CategoryDroppedError {
		return ""
	}
	return cat
}

func matchesEntry(entry Entry, f Finding) bool {
	entryDetail := entry.Detail
	if entryDetail == "" {
		entryDetail = entry.Callee
	}
	fDetail := f.Detail
	if fDetail == "" {
		fDetail = f.Callee
	}
	if entryDetail != fDetail || entry.EnclosingFunc != f.EnclosingFunc {
		return false
	}

	if normalizeCategory(entry.Category) != normalizeCategory(f.Category) {
		return false
	}

	entryFile := filepath.ToSlash(entry.File)
	if entryFile == filepath.ToSlash(f.RelFile) || entryFile == filepath.ToSlash(f.RepoRel) || entryFile == filepath.ToSlash(f.ModRel) || entryFile == filepath.ToSlash(f.Pos.Filename) {
		return true
	}
	if strings.HasSuffix(filepath.ToSlash(f.Pos.Filename), "/"+entryFile) {
		return true
	}
	return false
}

func isStaleCandidate(entry Entry, checkedFiles map[string]bool, rootDir string) bool {
	entryFile := filepath.ToSlash(entry.File)
	if checkedFiles != nil {
		if checkedFiles[entryFile] {
			return true
		}
	}
	if rootDir != "" {
		fullPath := filepath.Join(rootDir, filepath.FromSlash(entryFile))
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			return true
		}
	}
	return false
}

// ResolveBaselinePath determines the baseline file path given repoRoot, module dir,
// analyzer name, any explicitly configured path (from CLI or config), and whether write-baseline is requested.
func ResolveBaselinePath(repoRoot, dir, analyzer, configured string, writeBaseline bool) string {
	if configured != "" {
		if filepath.IsAbs(configured) {
			return configured
		}
		if repoRoot != "" {
			return filepath.Join(repoRoot, configured)
		}
		return filepath.Join(dir, configured)
	}

	root := repoRoot
	if root == "" {
		root = dir
	}

	defaultFilename := fmt.Sprintf("%s-baseline.txt", analyzer)
	defaultBaseline := filepath.Join(root, ".ap", defaultFilename)
	if fi, err := os.Stat(defaultBaseline); err == nil && !fi.IsDir() {
		return defaultBaseline
	}

	if dir != "" && dir != root {
		modBaseline := filepath.Join(dir, ".ap", defaultFilename)
		if fi, err := os.Stat(modBaseline); err == nil && !fi.IsDir() {
			return modBaseline
		}
	}

	if writeBaseline {
		return defaultBaseline
	}
	return ""
}

// FindEnclosingFunc returns the enclosing function or method name for pos in file.
func FindEnclosingFunc(file *ast.File, fset *token.FileSet, pos token.Pos) string {
	targetOffset := fset.Position(pos).Offset

	// Check function declarations first
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			start := fset.Position(fn.Pos()).Offset
			end := fset.Position(fn.End()).Offset
			if targetOffset >= start && targetOffset <= end {
				return FormatEnclosingFunc(fn)
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

// FormatEnclosingFunc formats an ast.FuncDecl into a function signature string.
func FormatEnclosingFunc(fn *ast.FuncDecl) string {
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		recv := fn.Recv.List[0].Type
		if star, ok := recv.(*ast.StarExpr); ok {
			return fmt.Sprintf("(*%s).%s", types.ExprString(star.X), fn.Name.Name)
		}
		return fmt.Sprintf("(%s).%s", types.ExprString(recv), fn.Name.Name)
	}
	return fn.Name.Name
}

// ApplyOptions configures baseline matching, reporting, and recording.
type ApplyOptions struct {
	Name            string
	RepoRoot        string
	Dir             string
	ConfiguredPath  string
	WriteBaseline   bool
	AllCheckedFiles map[string]bool
	Mode            string
	Stdout          io.Writer
	Stderr          io.Writer
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

// Apply handles baseline resolution, relative path normalization, deterministic sorting,
// baseline writing or matching, diagnostic reporting, and mode-based error generation.
func Apply(findings []Finding, opts ApplyOptions) ([]Finding, error) {
	baselineRoot := opts.RepoRoot
	if baselineRoot == "" {
		baselineRoot = opts.Dir
	}

	baselinePath := ResolveBaselinePath(opts.RepoRoot, opts.Dir, opts.Name, opts.ConfiguredPath, opts.WriteBaseline)
	if baselinePath != "" {
		bDir := filepath.Dir(baselinePath)
		if filepath.Base(bDir) == ".ap" {
			baselineRoot = filepath.Dir(bDir)
		}
	}

	for i := range findings {
		if findings[i].RelFile == "" {
			findings[i].RelFile = relPath(baselineRoot, findings[i].Pos.Filename)
		}
		if findings[i].RepoRel == "" {
			findings[i].RepoRel = relPath(opts.RepoRoot, findings[i].Pos.Filename)
		}
		if findings[i].ModRel == "" {
			findings[i].ModRel = relPath(opts.Dir, findings[i].Pos.Filename)
		}
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

	if opts.WriteBaseline {
		var existingEntries []Entry
		if baselinePath != "" {
			entries, err := Load(baselinePath)
			if err != nil {
				return nil, err
			}
			existingEntries = entries
		}

		hasChanges := len(findings) > 0
		var finalEntries []Entry
		for _, e := range existingEntries {
			entryFile := filepath.ToSlash(e.File)
			if !opts.AllCheckedFiles[entryFile] {
				finalEntries = append(finalEntries, e)
			} else {
				hasChanges = true
			}
		}

		if hasChanges {
			for _, f := range findings {
				relFile := relPath(baselineRoot, f.Pos.Filename)
				finalEntries = append(finalEntries, Entry{
					File:          relFile,
					EnclosingFunc: f.EnclosingFunc,
					Category:      f.Category,
					Detail:        f.Detail,
					Callee:        f.Detail,
				})
			}

			if baselinePath != "" {
				if err := Write(baselinePath, opts.Name, finalEntries); err != nil {
					return nil, fmt.Errorf("failed to write baseline to %s: %w", baselinePath, err)
				}
			}
			if opts.Stdout != nil {
				if _, err := fmt.Fprintf(opts.Stdout, "Wrote %d baseline entries to %s\n", len(finalEntries), baselinePath); err != nil {
					klog.V(2).Infof("failed to write baseline message: %v", err)
				}
			}
		}
		return nil, nil
	}

	var baselineEntries []Entry
	if baselinePath != "" {
		entries, err := Load(baselinePath)
		if err != nil {
			return nil, err
		}
		baselineEntries = entries
	}

	newFindings, staleEntries := Match(findings, baselineEntries, opts.AllCheckedFiles, baselineRoot)
	if opts.Stdout != nil {
		for _, stale := range staleEntries {
			if _, err := fmt.Fprintf(opts.Stdout, "stale baseline entry: %s (remove from baseline)\n", stale); err != nil {
				klog.V(2).Infof("failed to write stale entry: %v", err)
			}
		}
	}

	findingsToReport := newFindings
	if len(baselineEntries) == 0 {
		findingsToReport = findings
	}

	if opts.Stderr != nil {
		for _, f := range findingsToReport {
			msg := f.Message
			if msg == "" {
				msg = f.Detail
			}
			if _, err := fmt.Fprintf(opts.Stderr, "%s: %s\n", f.Pos, msg); err != nil {
				klog.V(2).Infof("failed to write diagnostic: %v", err)
			}
			for _, rel := range f.Related {
				if _, err := fmt.Fprintf(opts.Stderr, "%s:\t%s\n", rel.Pos, rel.Message); err != nil {
					klog.V(2).Infof("failed to write related info: %v", err)
				}
			}
		}
	}

	if len(findingsToReport) > 0 {
		if opts.Mode == "error" || opts.Mode == "" {
			return findingsToReport, fmt.Errorf("%s check failed in %s", opts.Name, opts.Dir)
		} else if opts.Mode == "warn" {
			klog.Warningf("%s check failed in %s: %d diagnostics found", opts.Name, opts.Dir, len(findingsToReport))
		}
	}
	return findingsToReport, nil
}
