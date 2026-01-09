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

	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

type Config struct {
	License         string `json:"license"`
	CopyrightHolder string `json:"copyrightHolder"`
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

func (p *processor) shouldIgnoreFile(path string) bool {
	for _, pattern := range p.options.IgnoreFiles {
		// Check if matches pattern, for now we just check for prefix
		if strings.HasPrefix(path, pattern) {
			return true
		}
	}

	return false
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

	// TODO: Should we merge config into options?

	processor := &processor{
		config:  config,
		options: opt,
	}

	if len(files) == 0 {
		if err := filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() {
				// Make path relative to repoRoot for consistency if needed,
				// or just use absolute paths.
				// The original code used filepath.Walk(".") after Chdir(repoRoot).
				// Here we are walking repoRoot.
				// To match original behavior of checking ignore patterns (which look like relative paths),
				// we might want to make it relative.
				relPath, err := filepath.Rel(repoRoot, path)
				if err != nil {
					return err
				}
				files = append(files, relPath)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("error walking directory: %w", err)
		}
	}

	// Ensure we are in repoRoot so relative paths work, or use absolute paths.
	// The original code did os.Chdir(repoRoot).
	// Let's do that for safety if the caller hasn't.
	// But changing global CWD in a library function is bad.
	// Instead, let's construct absolute paths or assume CWD is repoRoot?
	// The issue says "codestyle command... looks for .codestyle/...".. 
	// Let's assume the caller sets the CWD or we handle paths correctly.
	// For now, let's use the full path for reading/writing, but use relative path for ignore checks?

	for _, file := range files {
		// existing logic expects file to be relative or at least checkable against ignore patterns.
		// If `files` came from Walk above, they are relative.
		// If `files` passed in, they might be whatever user typed.
		// Let's normalize to relative to repoRoot for checking ignore, and absolute for IO.

		absPath := file
		if !filepath.IsAbs(file) {
			absPath = filepath.Join(repoRoot, file)
		}

		relPath, err := filepath.Rel(repoRoot, absPath)
		if err != nil {
			// If we can't make it relative to repo root, maybe it's outside?
			// Just skip or log?
			log.Info("Skipping file outside repo root", "file", file)
			continue
		}

		if err := processor.processFile(ctx, absPath, relPath); err != nil {
			log.Error(err, "Error processing file", "file", file)
			errs = append(errs, fmt.Errorf("error processing %s: %w", file, err))
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

type processor struct {
	options FileHeadersOptions
	config  *Config
}

func (p *processor) processFile(ctx context.Context, absPath, relPath string) error {
	log := klog.FromContext(ctx)

	if p.shouldIgnoreFile(relPath) {
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

	// Ensure we don't end up with double newlines at EOF if original had one?
	// Join usually handles separators.

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
