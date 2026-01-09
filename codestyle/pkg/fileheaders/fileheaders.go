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

type Options struct {
	IgnoreFiles []string `json:"ignore"`
	RepoRoot    string
	Files       []string
}

func (o *Options) InitDefaults() {
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

func Run(ctx context.Context, options *Options) error {
	var errs []error

	log := klog.FromContext(ctx)

	if options.RepoRoot == "" {
		repoRoot, err := findRepoRoot()
		if err != nil {
			return err
		}
		options.RepoRoot = repoRoot
	}

	if err := os.Chdir(options.RepoRoot); err != nil {
		return fmt.Errorf("error changing to repo root %s: %w", options.RepoRoot, err)
	}

	configFile := filepath.Join(options.RepoRoot, ".codestyle/file-headers.yaml")
	config, err := loadConfig(configFile)
	if err != nil {
		return err
	}

	processor := &processor{
		config:  config,
		options: options,
	}

	files := options.Files
	if len(files) == 0 {
		if err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() {
				files = append(files, path)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("error walking directory: %w", err)
		}
	}

	for _, file := range files {
		if err := processor.processFile(ctx, file); err != nil {
			log.Error(err, "Error processing file", "file", file)
			err = fmt.Errorf("error processing %s: %w", file, err)
			errs = append(errs, err)
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
	options *Options
	config  *Config
}

func (p *processor) processFile(ctx context.Context, path string) error {
	log := klog.FromContext(ctx)

	if p.shouldIgnoreFile(path) {
		return nil
	}

	ext := filepath.Ext(path)
	commentStyle := getCommentStyle(filepath.Base(path), ext)
	if commentStyle == "" {
		return nil
	}

	content, err := os.ReadFile(path)
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

	log.Info("Adding file header", "file", path)

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
	return os.WriteFile(path, []byte(output), 0644)
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

// findRepoRoot attempts to find the root of the git repository
func findRepoRoot() (string, error) {
	startDir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	dir := startDir
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", fmt.Errorf("could not find git repository root (starting at %s)", startDir)
}
