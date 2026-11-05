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

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Effective returns the effective configuration for dir, taking into account
// the base configuration at repoRoot and any enclosing subtree .ap/go.yaml
// overrides walking from dir up to repoRoot.
func Effective(repoRoot, dir string) (*Config, error) {
	cleanRepoRoot := filepath.Clean(repoRoot)
	cleanDir := filepath.Clean(dir)
	if !filepath.IsAbs(cleanDir) {
		cleanDir = filepath.Clean(filepath.Join(cleanRepoRoot, cleanDir))
	}

	base, err := Load(cleanRepoRoot)
	if err != nil {
		return nil, err
	}
	effective := base.clone()

	// Collect enclosing subtree .ap/go.yaml files from dir up to repoRoot
	var subtreeFiles []string
	for curr := cleanDir; curr != cleanRepoRoot && strings.HasPrefix(curr, cleanRepoRoot); curr = filepath.Dir(curr) {
		goFile := filepath.Join(curr, ".ap", "go.yaml")
		if fi, err := os.Stat(goFile); err == nil && !fi.IsDir() {
			subtreeFiles = append(subtreeFiles, goFile)
		} else if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("error checking %s: %w", goFile, err)
		}
	}

	// Reverse to apply top-down (outermost ancestor first, nearest last)
	for i, j := 0, len(subtreeFiles)-1; i < j; i, j = i+1, j-1 {
		subtreeFiles[i], subtreeFiles[j] = subtreeFiles[j], subtreeFiles[i]
	}

	for _, goFile := range subtreeFiles {
		data, err := os.ReadFile(goFile)
		if err != nil {
			return nil, fmt.Errorf("error reading %s: %w", goFile, err)
		}
		var override Config
		if err := UnmarshalStrict(data, &override); err != nil {
			return nil, fmt.Errorf("error parsing %s: %w", goFile, err)
		}
		effective.apply(&override)
	}

	if err := effective.Validate(); err != nil {
		return nil, err
	}

	return effective, nil
}

func (c *Config) clone() *Config {
	if c == nil {
		return nil
	}
	out := *c
	if c.Gofmt != nil {
		g := *c.Gofmt
		out.Gofmt = &g
	}
	if c.Govet != nil {
		g := *c.Govet
		out.Govet = &g
	}
	if c.Govulncheck != nil {
		g := *c.Govulncheck
		out.Govulncheck = &g
	}
	if c.Tidy != nil {
		t := *c.Tidy
		out.Tidy = &t
	}
	if c.Skip != nil {
		out.Skip = append([]string(nil), c.Skip...)
	}
	if c.Lint != nil {
		out.Lint = c.Lint.clone()
	}
	return &out
}

func (l *LintConfig) clone() *LintConfig {
	if l == nil {
		return nil
	}
	out := *l
	if l.GOOS != nil {
		out.GOOS = append([]string(nil), l.GOOS...)
	}
	if l.linters != nil {
		out.linters = make(map[string]LinterConfig, len(l.linters))
		for k, v := range l.linters {
			out.linters[k] = cloneLinter(v)
		}
	}
	return &out
}

func cloneLinter(v LinterConfig) LinterConfig {
	switch c := v.(type) {
	case *CommonLinterConfig:
		val := *c
		return &val
	case *DroppedErrorsConfig:
		val := *c
		if c.Exclude != nil {
			val.Exclude = append([]string(nil), c.Exclude...)
		}
		return &val
	default:
		return v
	}
}

func (c *Config) apply(o *Config) {
	if o.Gofmt != nil {
		c.Gofmt = o.Gofmt
	}
	if o.Govet != nil {
		c.Govet = o.Govet
	}
	if o.Govulncheck != nil {
		c.Govulncheck = o.Govulncheck
	}
	if o.Tidy != nil {
		c.Tidy = o.Tidy
	}
	if o.Skip != nil {
		c.Skip = o.Skip
	}
	if o.Toolchain != "" {
		c.Toolchain = o.Toolchain
	}
	if o.Lint != nil {
		if c.Lint == nil {
			c.Lint = &LintConfig{}
		}
		c.Lint.apply(o.Lint)
	}
}

func (l *LintConfig) apply(o *LintConfig) {
	if o.SkipGenerated != nil {
		l.SkipGenerated = o.SkipGenerated
	}
	if o.SkipTests != nil {
		l.SkipTests = o.SkipTests
	}
	if o.GOOS != nil {
		l.GOOS = o.GOOS
	}
	for name, cfg := range o.linters {
		if cfg == nil {
			continue
		}
		if l.linters == nil {
			l.linters = make(map[string]LinterConfig)
		}
		if existing, ok := l.linters[name]; ok && existing != nil {
			l.linters[name] = applyLinter(existing, cfg)
		} else {
			l.linters[name] = cfg
		}
	}
}

func applyLinter(dst, src LinterConfig) LinterConfig {
	switch d := dst.(type) {
	case *CommonLinterConfig:
		if s, ok := src.(*CommonLinterConfig); ok {
			applyCommon(d, s)
			return d
		}
	case *DroppedErrorsConfig:
		if s, ok := src.(*DroppedErrorsConfig); ok {
			applyCommon(&d.CommonLinterConfig, &s.CommonLinterConfig)
			if s.Exclude != nil {
				d.Exclude = s.Exclude
			}
			if s.UseDefaultExcludes != nil {
				d.UseDefaultExcludes = s.UseDefaultExcludes
			}
			return d
		}
	}
	return src
}

func applyCommon(d, s *CommonLinterConfig) {
	if s.Mode != "" {
		d.Mode = s.Mode
	}
	if s.SkipGenerated != nil {
		d.SkipGenerated = s.SkipGenerated
	}
	if s.SkipTests != nil {
		d.SkipTests = s.SkipTests
	}
	if s.Baseline != "" {
		d.Baseline = s.Baseline
	}
}
