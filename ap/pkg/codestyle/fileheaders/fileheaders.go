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

package fileheaders

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/codestyle/walker"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/scoped"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
)

func hasK8sStyleBlockHeader(buf []byte) bool {
	idx := bytes.Index(buf, []byte("/*"))
	return idx != -1 && bytes.Contains(buf[idx:], []byte("Copyright"))
}

func isYearOrRange(s string) bool {
	if s == "" {
		return false
	}
	hasDigit := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			hasDigit = true
		} else if c == ',' || c == '-' || c == ' ' || c == '\t' {
			// separator
		} else {
			return false
		}
	}
	return hasDigit
}

func matchCopyrightLine(line string, copyrightHolder string) bool {
	if !strings.HasPrefix(line, "Copyright") {
		return false
	}
	rest := line[len("Copyright"):]
	if len(rest) == 0 || (rest[0] != ' ' && rest[0] != '\t') {
		return false
	}
	rest = strings.TrimSpace(rest)

	var yearPart string
	if copyrightHolder != "" {
		if !strings.HasSuffix(rest, copyrightHolder) {
			return false
		}
		beforeHolder := rest[:len(rest)-len(copyrightHolder)]
		if len(beforeHolder) == 0 || (beforeHolder[len(beforeHolder)-1] != ' ' && beforeHolder[len(beforeHolder)-1] != '\t') {
			return false
		}
		yearPart = strings.TrimSpace(beforeHolder)
	} else {
		yearPart = rest
	}

	return isYearOrRange(yearPart)
}

func match13Lines(lines []string, copyrightHolder string) bool {
	if len(lines) != 13 {
		return false
	}
	if !matchCopyrightLine(lines[0], copyrightHolder) {
		return false
	}
	if lines[1] != "" {
		return false
	}
	if lines[2] != `Licensed under the Apache License, Version 2.0 (the "License");` {
		return false
	}
	if lines[3] != "you may not use this file except in compliance with the License." &&
		lines[3] != "You may not use this file except in compliance with the License." {
		return false
	}
	if lines[4] != "you may obtain a copy of the License at" &&
		lines[4] != "You may obtain a copy of the License at" {
		return false
	}
	if lines[5] != "" {
		return false
	}
	if lines[6] != "http://www.apache.org/licenses/LICENSE-2.0" {
		return false
	}
	if lines[7] != "" {
		return false
	}
	if lines[8] != "Unless required by applicable law or agreed to in writing, software" {
		return false
	}
	if lines[9] != `distributed under the License is distributed on an "AS IS" BASIS,` {
		return false
	}
	if lines[10] != "WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied." {
		return false
	}
	if lines[11] != "See the License for the specific language governing permissions and" {
		return false
	}
	if lines[12] != "limitations under the License." {
		return false
	}
	return true
}

func matchExpectedHeader(header string, style string, copyrightHolder string) bool {
	rawLines := strings.Split(header, "\n")
	if len(rawLines) < 13 {
		return false
	}

	lines := make([]string, len(rawLines))
	hasStyle := make([]bool, len(rawLines))
	for i, raw := range rawLines {
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, style) {
			hasStyle[i] = true
			lines[i] = strings.TrimSpace(trimmed[len(style):])
		} else {
			lines[i] = trimmed
		}
	}

	for start := 0; start <= len(lines)-13; start++ {
		allHaveStyle := true
		for j := 0; j < 13; j++ {
			if !hasStyle[start+j] {
				allHaveStyle = false
				break
			}
		}
		if allHaveStyle && match13Lines(lines[start:start+13], copyrightHolder) {
			return true
		}
	}
	return false
}

type FileHeadersOptions struct {
	IgnoreFiles []string `json:"ignore"`
}

func (o *FileHeadersOptions) InitDefaults() {
	o.IgnoreFiles = []string{
		".git/",
		".svn/",
		".hg/",
		"vendor/",
		"third_party/",
		"node_modules/",
	}
}

// processor handles file processing
type processor struct {
	config     *config.HeadersConfig
	ignoreList *walker.IgnoreList
}

func (p *processor) shouldIgnoreFile(relPath string, isDir bool) bool {
	return p.ignoreList.ShouldIgnore(relPath, isDir)
}

func Run(ctx context.Context, r *sourcetree.SourceTree, repoRoot string, files []string) error {
	var errs []error

	var opt FileHeadersOptions
	opt.InitDefaults()

	log := scoped.Log(ctx)

	configFile := filepath.Join(repoRoot, ".ap/headers.yaml")
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		log.Log(ctx, scoped.LevelVerbose2, "no .ap/headers.yaml found, skipping file headers")
		return nil
	}

	config, err := config.LoadHeaders(repoRoot)
	if err != nil {
		return err
	}

	if config.License == "" || config.License == "none" {
		log.Log(ctx, scoped.LevelVerbose2, "license is empty or 'none', skipping file headers")
		return nil
	}
	if config.License != "apache-2.0" {
		return fmt.Errorf("unsupported license %q in .ap/headers.yaml (supported: %q, %q)", config.License, "apache-2.0", "none")
	}

	// Combine default ignores with config skips
	allIgnores := append(opt.IgnoreFiles, config.Skip...)
	ignoreList := walker.NewIgnoreList(allIgnores)

	processor := &processor{
		config:     config,
		ignoreList: ignoreList,
	}

	expandedFiles, err := walker.ExpandPaths(r, repoRoot, files, ignoreList)
	if err != nil {
		return err
	}

	for _, absPath := range expandedFiles {
		relPath, err := filepath.Rel(repoRoot, absPath)
		if err != nil {
			log.Error("skipping file outside repo root", "error", err, "file", absPath)
			errs = append(errs, fmt.Errorf("skipping file outside repo root %s: %w", absPath, err))
			continue
		}

		if err := processor.processFile(ctx, absPath, relPath); err != nil {
			log.Error("error processing file", "error", err, "file", absPath)
			errs = append(errs, fmt.Errorf("error processing %s: %w", absPath, err))
		}
	}
	return errors.Join(errs...)
}

func (p *processor) processFile(ctx context.Context, absPath, relPath string) error {
	log := scoped.Log(ctx)

	if p.shouldIgnoreFile(relPath, false) {
		return nil
	}

	ext := filepath.Ext(absPath)
	commentStyle := getCommentStyle(filepath.Base(absPath), ext)
	if commentStyle == "" {
		return nil
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return err
	}

	// Check for generated file
	// We check the first 2000 bytes to be efficient
	checkBuf := content
	if len(checkBuf) > 2000 {
		checkBuf = checkBuf[:2000]
	}

	if p.config.SkipGenerated != nil && *p.config.SkipGenerated {
		if sourcetree.CheckIsGenerated(absPath) {
			return nil
		}
	}

	// Check for K8s style block headers in Go files
	if ext == ".go" {
		// Look for /* ... Copyright ... */ pattern
		if hasK8sStyleBlockHeader(checkBuf) {
			return nil
		}
	}

	lines := strings.Split(string(content), "\n")

	// Find the start index for comments (skipping shebang and empty lines)
	startIdx := 0
	if len(lines) > 0 && strings.HasPrefix(lines[0], "#!") {
		startIdx = 1
	}
	for startIdx < len(lines) && strings.TrimSpace(lines[startIdx]) == "" {
		startIdx++
	}

	// Now locate a potential copyright header block starting from startIdx
	var commentLines []string
	headerStartIdx := -1
	headerEndIdx := -1

	for i := startIdx; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(commentLines) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(trimmed, commentStyle) {
			if len(commentLines) == 0 {
				headerStartIdx = i
			}
			commentLines = append(commentLines, line)
			headerEndIdx = i + 1
		} else {
			if len(commentLines) > 0 {
				break
			}
			break
		}
	}

	joinedComments := ""
	if len(commentLines) > 0 {
		joinedComments = strings.Join(commentLines, "\n")
	}

	// Validate strictly against the contiguous leading comment block at the top of the file
	if matchExpectedHeader(joinedComments, commentStyle, p.config.CopyrightHolder) {
		return nil
	}

	log.Info("updating or adding file header", "file", relPath)

	header, err := GenerateHeader(p.config, commentStyle)
	if err != nil {
		return err
	}

	foundHeaderBlock := false
	if len(commentLines) > 0 {
		if strings.Contains(strings.ToLower(joinedComments), "copyright") {
			foundHeaderBlock = true
		}
	}

	if !foundHeaderBlock {
		headerStartIdx = startIdx
		headerEndIdx = startIdx
	} else {
		// Replace the invalid copyright header block.
		// If there is an empty line immediately following the old header, skip it
		// because the generated header already ends with a trailing newline (blank line).
		if headerEndIdx < len(lines) && strings.TrimSpace(lines[headerEndIdx]) == "" {
			headerEndIdx++
		}
	}

	var newLines []string
	newLines = append(newLines, lines[:headerStartIdx]...)
	headerLines := strings.Split(header, "\n")
	newLines = append(newLines, headerLines...)
	newLines = append(newLines, lines[headerEndIdx:]...)

	output := strings.Join(newLines, "\n")
	return os.WriteFile(absPath, []byte(output), 0644)
}

func getCommentStyle(name, ext string) string {
	if name == "Dockerfile" {
		return "#"
	}
	switch ext {
	case ".go":
		return "//"
	case ".yaml", ".yml", ".sh", ".py", ".tf", ".toml":
		return "#"
	}
	return ""
}

func GenerateHeader(cfg *config.HeadersConfig, style string) (string, error) {
	if cfg == nil || cfg.License == "" || cfg.License == "none" {
		return "", nil
	}

	if cfg.License != "apache-2.0" {
		return "", fmt.Errorf("unsupported license %q in .ap/headers.yaml (supported: %q, %q)", cfg.License, "apache-2.0", "none")
	}

	year := time.Now().Year()

	var lines []string
	if cfg.CopyrightHolder != "" {
		lines = append(lines, fmt.Sprintf("%s Copyright %d %s", style, year, cfg.CopyrightHolder))
	} else {
		lines = append(lines, fmt.Sprintf("%s Copyright %d", style, year))
	}
	lines = append(lines, style)
	lines = append(lines, fmt.Sprintf("%s Licensed under the Apache License, Version 2.0 (the \"License\");", style))
	lines = append(lines, fmt.Sprintf("%s you may not use this file except in compliance with the License.", style))
	lines = append(lines, fmt.Sprintf("%s You may obtain a copy of the License at", style))
	lines = append(lines, style)
	lines = append(lines, fmt.Sprintf("%s     http://www.apache.org/licenses/LICENSE-2.0", style))
	lines = append(lines, style)
	lines = append(lines, fmt.Sprintf("%s Unless required by applicable law or agreed to in writing, software", style))
	lines = append(lines, fmt.Sprintf("%s distributed under the License is distributed on an \"AS IS\" BASIS,", style))
	lines = append(lines, fmt.Sprintf("%s WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.", style))
	lines = append(lines, fmt.Sprintf("%s See the License for the specific language governing permissions and", style))
	lines = append(lines, fmt.Sprintf("%s limitations under the License.", style))
	lines = append(lines, "")

	return strings.Join(lines, "\n"), nil
}
