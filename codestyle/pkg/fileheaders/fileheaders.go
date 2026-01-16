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
	"regexp"
	"strings"
	"time"

	"github.com/gke-labs/gke-labs-infra/codestyle/pkg/walker"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

type Config struct {
	License         string   `json:"license"`
	CopyrightHolder string   `json:"copyrightHolder"`
	Skip            []string `json:"skip"`
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
	config     *Config
	ignoreList *walker.IgnoreList
}

func (p *processor) shouldIgnoreFile(relPath string, isDir bool) bool {
	return p.ignoreList.ShouldIgnore(relPath, isDir)
}

func Run(ctx context.Context, repoRoot string, files []string) error {
	var errs []error

	var opt FileHeadersOptions
	opt.InitDefaults()

	log := klog.FromContext(ctx)

	configFile := filepath.Join(repoRoot, ".codestyle/file-headers.yaml")
	config, err := loadConfig(configFile)
	if err != nil {
		return err
	}

	// Combine default ignores with config skips
	allIgnores := append(opt.IgnoreFiles, config.Skip...)
	ignoreList := walker.NewIgnoreList(allIgnores)

	processor := &processor{
		config:     config,
		ignoreList: ignoreList,
	}

	if len(files) == 0 {
		fv := walker.NewFileView(repoRoot, allIgnores)
		err := fv.Walk(func(f walker.File) error {
			// f.RelPath is already relative to repoRoot
			if err := processor.processFile(ctx, f.Path, f.RelPath); err != nil {
				log.Error(err, "Error processing file", "file", f.RelPath)
				// We don't abort walk on individual file error usually, but Walk signature expects error.
				// We should collect errors.
				errs = append(errs, fmt.Errorf("error processing %s: %w", f.RelPath, err))
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("error walking directory: %w", err)
		}
	} else {
		// Ensure we use absolute paths for IO, but relative paths for ignore checks.
		for _, file := range files {
			absPath := file
			if !filepath.IsAbs(file) {
				absPath = filepath.Join(repoRoot, file)
			}

			relPath, err := filepath.Rel(repoRoot, absPath)
			if err != nil {
				log.Error(err, "Skipping file outside repo root", "file", file)
				errs = append(errs, fmt.Errorf("skipping file outside repo root %s: %w", file, err))
				continue
			}

			if err := processor.processFile(ctx, absPath, relPath); err != nil {
				log.Error(err, "Error processing file", "file", file)
				errs = append(errs, fmt.Errorf("error processing %s: %w", file, err))
			}
		}
	}
	return errors.Join(errs...)
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func (p *processor) processFile(ctx context.Context, absPath, relPath string) error {
	log := klog.FromContext(ctx)

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

	// Robust check: look for the copyright string with the comment prefix
	// We check the first 2000 bytes to be efficient and avoid false positives (like finding the string in the code itself)
	checkBuf := content
	if len(checkBuf) > 2000 {
		checkBuf = checkBuf[:2000]
	}

	expectedCopyright := commentStyle + " Copyright"
	if bytes.Contains(checkBuf, []byte(expectedCopyright)) {
		return nil
	}

	// Check for K8s style block headers in Go files
	if ext == ".go" {
		// Look for /* ... Copyright ... */ pattern
		// We use a simplified regex that looks for /* followed by Copyright within the buffer
		if regexp.MustCompile(`(?s)/\*.*?Copyright`).Match(checkBuf) {
			return nil
		}
	}

	log.Info("Adding file header", "file", relPath)

	header, err := p.generateHeader(commentStyle)
	if err != nil {
		return err
	}
	lines := strings.Split(string(content), "\n")
	var newLines []string

	hasShebang := len(lines) > 0 && strings.HasPrefix(lines[0], "#!")

	if hasShebang {
		newLines = append(newLines, lines[0])
		newLines = append(newLines, "")
		newLines = append(newLines, header)
		if len(lines) > 1 {
			newLines = append(newLines, lines[1:]...)
		}
	} else {
		newLines = append(newLines, header)
		newLines = append(newLines, lines...)
	}

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

func (p *processor) generateHeader(style string) (string, error) {
	year := time.Now().Year()

	if p.config.License != "apache-2.0" {
		return "", fmt.Errorf("unsupported license: %s", p.config.License)
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("%s Copyright %d %s", style, year, p.config.CopyrightHolder))
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
