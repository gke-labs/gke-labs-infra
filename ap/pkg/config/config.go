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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Config struct {
	Gofmt       *GofmtConfig       `json:"gofmt"`
	Govet       *GovetConfig       `json:"govet"`
	Govulncheck *GovulncheckConfig `json:"govulncheck"`
	Tidy        *TidyConfig        `json:"tidy"`
	Skip        []string           `json:"skip"`
	Lint        *LintConfig        `json:"lint"`
	Toolchain   string             `json:"toolchain"`
}

type GofmtConfig struct {
	Enabled *bool `json:"enabled"`
}

type GovetConfig struct {
	Enabled *bool `json:"enabled"`
}

type GovulncheckConfig struct {
	Enabled *bool `json:"enabled"`
}

type TidyConfig struct {
	Enabled *bool `json:"enabled"`
}

type LintConfig struct {
	SkipGenerated *bool                     `json:"skipGenerated,omitempty"`
	SkipTests     *bool                     `json:"skipTests,omitempty"`
	GOOS          []string                  `json:"goos,omitempty"`
	Analyzers     map[string]AnalyzerConfig `json:"analyzers,omitempty"`
}

type AnalyzerConfig struct {
	Mode               string   `json:"mode,omitempty"`
	SkipGenerated      *bool    `json:"skipGenerated,omitempty"`
	SkipTests          *bool    `json:"skipTests,omitempty"`
	Baseline           string   `json:"baseline,omitempty"`
	Exclude            []string `json:"exclude,omitempty"`
	UseDefaultExcludes *bool    `json:"useDefaultExcludes,omitempty"`
	Types              []string `json:"types,omitempty"`
	Packages           []string `json:"packages,omitempty"`
}

// Linter returns the AnalyzerConfig for the named linter.
// It panics if the linter name is not registered.
// If the linter is not explicitly configured, it returns a nil-safe *AnalyzerConfig.
func (c *Config) Linter(name string) *AnalyzerConfig {
	if !isAnalyzer(name) {
		panic(fmt.Sprintf("unknown linter %q", name))
	}
	if c == nil || c.Lint == nil || c.Lint.Analyzers == nil {
		return (*AnalyzerConfig)(nil)
	}
	if analyzerConfig, ok := c.Lint.Analyzers[name]; ok {
		return &analyzerConfig
	}
	return (*AnalyzerConfig)(nil)
}

// DeepCopy returns a deep copy of c using JSON marshal and unmarshal.
func (c *Config) DeepCopy() *Config {
	if c == nil {
		return nil
	}
	data, err := json.Marshal(c)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal Config in DeepCopy: %v", err))
	}
	var copyConfig Config
	if err := json.Unmarshal(data, &copyConfig); err != nil {
		panic(fmt.Sprintf("failed to unmarshal Config in DeepCopy: %v", err))
	}
	return &copyConfig
}

// Load loads the configuration from .ap/go.yaml in the repository root.
func Load(repoRoot string) (*Config, error) {
	configFile := filepath.Join(repoRoot, ".ap/go.yaml")

	var config Config
	if _, err := LoadYAML(configFile, &config); err != nil {
		return nil, err
	}

	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("error parsing %s: %w", configFile, err)
	}

	return &config, nil
}

// Validate checks the configuration for semantic errors.
func (c *Config) Validate() error {
	if c.Lint != nil {
		checkMode := func(name, mode string) error {
			if mode != "" && mode != "error" && mode != "warn" && mode != "ignore" {
				return fmt.Errorf("invalid %s mode %q: must be one of 'error', 'warn', 'ignore'", name, mode)
			}
			return nil
		}
		if c.Lint.Analyzers != nil {
			var names []string
			for analyzerName := range c.Lint.Analyzers {
				names = append(names, analyzerName)
			}
			sort.Strings(names)

			known := RegisteredAnalyzers()
			for _, name := range names {
				if !isAnalyzer(name) {
					return fmt.Errorf("unknown analyzer %q (valid analyzers: %s)", name, strings.Join(known, ", "))
				}
				analyzerConfig := c.Lint.Analyzers[name]
				if err := checkMode(name, analyzerConfig.GetMode()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// IsGofmtEnabled returns true if gofmt is enabled in the config (defaulting to true).
func (c *Config) IsGofmtEnabled() bool {
	if c.Gofmt != nil && c.Gofmt.Enabled != nil {
		return *c.Gofmt.Enabled
	}
	return true
}

// IsGovetEnabled returns true if govet is enabled in the config (defaulting to true).
func (c *Config) IsGovetEnabled() bool {
	if c.Govet != nil && c.Govet.Enabled != nil {
		return *c.Govet.Enabled
	}
	return true
}

// IsGovulncheckEnabled returns true if govulncheck is enabled in the config (defaulting to true).
func (c *Config) IsGovulncheckEnabled() bool {
	if c.Govulncheck != nil && c.Govulncheck.Enabled != nil {
		return *c.Govulncheck.Enabled
	}
	return true
}

// IsTidyEnabled returns true if go mod tidy is enabled in the config (defaulting to true).
func (c *Config) IsTidyEnabled() bool {
	if c != nil && c.Tidy != nil && c.Tidy.Enabled != nil {
		return *c.Tidy.Enabled
	}
	return true
}

// IsDroppedErrorsEnabled returns true if dropped error checking is enabled (defaulting to true, mode != "ignore").
func (c *Config) IsDroppedErrorsEnabled() bool {
	return c.Linter("droppedErrors").IsEnabled()
}

// IsDroppedErrorsError returns true if dropped errors should be reported as an error.
// Default is true ("error" mode).
func (c *Config) IsDroppedErrorsError() bool {
	return c.DroppedErrorsMode() == "error"
}

// DroppedErrorsMode returns the mode for dropped error checking: "ignore", "warn", or "error".
// Default is "error".
func (c *Config) DroppedErrorsMode() string {
	mode := c.Linter("droppedErrors").GetMode()
	if mode != "" {
		return mode
	}
	return "error"
}

// DroppedErrors returns the configuration for droppedErrors, or nil if not configured.
func (c *Config) DroppedErrors() *AnalyzerConfig {
	if c == nil || c.Lint == nil || c.Lint.Analyzers == nil {
		return nil
	}
	if droppedErrorsConfig, ok := c.Lint.Analyzers["droppedErrors"]; ok {
		return &droppedErrorsConfig
	}
	return nil
}

// DroppedErrorsBaseline returns the configured baseline file path, or empty string.
func (c *Config) DroppedErrorsBaseline() string {
	if droppedErrors := c.DroppedErrors(); droppedErrors != nil {
		return droppedErrors.Baseline
	}
	return ""
}

// DroppedErrorsExclude returns the configured excluded symbols.
func (c *Config) DroppedErrorsExclude() []string {
	if droppedErrors := c.DroppedErrors(); droppedErrors != nil {
		return droppedErrors.Exclude
	}
	return nil
}

// DroppedErrorsUseDefaultExcludes returns true if default exclusions should be used (default true).
func (c *Config) DroppedErrorsUseDefaultExcludes() bool {
	if droppedErrors := c.DroppedErrors(); droppedErrors != nil && droppedErrors.UseDefaultExcludes != nil {
		return *droppedErrors.UseDefaultExcludes
	}
	return true
}

// GetMode returns the configured mode, or empty string if unset.
func (c *AnalyzerConfig) GetMode() string {
	if c == nil {
		return ""
	}
	return c.Mode
}

// GetBaseline returns the configured baseline file path, or empty string if unset.
func (c *AnalyzerConfig) GetBaseline() string {
	if c == nil {
		return ""
	}
	return c.Baseline
}

// IsEnabled returns true if the linter is enabled (defaulting to true, mode != "ignore").
func (c *AnalyzerConfig) IsEnabled() bool {
	if c != nil && c.Mode != "" {
		return c.Mode != "ignore"
	}
	return true
}

// IsError returns true if violations should be reported as errors (defaulting to true, mode == "error").
func (c *AnalyzerConfig) IsError() bool {
	if c != nil && c.Mode != "" {
		return c.Mode == "error"
	}
	return true
}

// SkipGeneratedOverride returns the configured skipGenerated override, or nil if unset.
func (c *AnalyzerConfig) SkipGeneratedOverride() *bool {
	if c == nil {
		return nil
	}
	return c.SkipGenerated
}

// SkipTestsOverride returns the configured skipTests override, or nil if unset.
func (c *AnalyzerConfig) SkipTestsOverride() *bool {
	if c == nil {
		return nil
	}
	return c.SkipTests
}

// IsLintSkipGenerated returns true if generated files should be skipped across lint checks (default false).
func (c *Config) IsLintSkipGenerated() bool {
	if c.Lint != nil && c.Lint.SkipGenerated != nil {
		return *c.Lint.SkipGenerated
	}
	return false
}

// IsLintSkipTests returns true if test files should be skipped across lint checks (default false).
func (c *Config) IsLintSkipTests() bool {
	if c.Lint != nil && c.Lint.SkipTests != nil {
		return *c.Lint.SkipTests
	}
	return false
}

// ResolveSkipGenerated resolves whether generated files should be skipped,
// checking a per-check override first, then lint.skipGenerated, then analyzerDefault.
func (c *Config) ResolveSkipGenerated(analyzerConfig *AnalyzerConfig, analyzerDefault bool) bool {
	var override *bool
	if analyzerConfig != nil {
		override = analyzerConfig.SkipGeneratedOverride()
	}
	var shared *bool
	if c != nil && c.Lint != nil {
		shared = c.Lint.SkipGenerated
	}
	return resolve(override, shared, analyzerDefault)
}

// ResolveSkipTests resolves whether test files should be skipped,
// checking a per-check override first, then lint.skipTests, then analyzerDefault.
func (c *Config) ResolveSkipTests(analyzerConfig *AnalyzerConfig, analyzerDefault bool) bool {
	var override *bool
	if analyzerConfig != nil {
		override = analyzerConfig.SkipTestsOverride()
	}
	var shared *bool
	if c != nil && c.Lint != nil {
		shared = c.Lint.SkipTests
	}
	return resolve(override, shared, analyzerDefault)
}

func resolve(override *bool, shared *bool, analyzerDefault bool) bool {
	if override != nil {
		return *override
	}
	if shared != nil {
		return *shared
	}
	return analyzerDefault
}

// DroppedErrorsSkipTests returns true if test files should be skipped (default true).
func (c *Config) DroppedErrorsSkipTests() bool {
	return c.ResolveSkipTests(c.Linter("droppedErrors"), true)
}

// DroppedErrorsSkipGenerated returns true if generated files should be skipped (default false).
func (c *Config) DroppedErrorsSkipGenerated() bool {
	return c.ResolveSkipGenerated(c.Linter("droppedErrors"), false)
}

// LintGOOS returns the list of target GOOS values configured under lint.goos,
// or nil if omitted (which implies automatic detection).
func (c *Config) LintGOOS() []string {
	if c == nil || c.Lint == nil {
		return nil
	}
	return c.Lint.GOOS
}

// ImageRepo returns the image repository to use, defaulting to "images.local".
func (c *Config) ImageRepo() string {
	repo := os.Getenv("IMAGE_PREFIX")
	if repo == "" {
		return "images.local"
	}
	return repo
}

type HeadersConfig struct {
	License         string   `json:"license"`
	CopyrightHolder string   `json:"copyrightHolder"`
	Skip            []string `json:"skip"`
	SkipGenerated   *bool    `json:"skipGenerated"`
}

func LoadHeaders(repoRoot string) (*HeadersConfig, error) {
	configFile := filepath.Join(repoRoot, ".ap/headers.yaml")
	var config HeadersConfig
	if _, err := LoadYAML(configFile, &config); err != nil {
		return nil, err
	}

	if config.SkipGenerated == nil {
		t := true
		config.SkipGenerated = &t
	}
	return &config, nil
}

// ImagesConfig represents the image build configuration, loaded from .ap/images.yaml.
type ImagesConfig struct {
	Platforms []string `json:"platforms"`
}

// LoadImagesConfig loads the configuration from .ap/images.yaml in the specified root directory.
func LoadImagesConfig(root string) (*ImagesConfig, error) {
	configFile := filepath.Join(root, ".ap/images.yaml")

	var config ImagesConfig
	if _, err := LoadYAML(configFile, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

// GetPlatforms returns the configured platforms, defaulting to ["linux/amd64", "linux/arm64"] if none are specified,
// and normalizing short names like "amd64" or "arm64" to "linux/amd64" or "linux/arm64".
func (c *ImagesConfig) GetPlatforms() []string {
	if len(c.Platforms) == 0 {
		return []string{"linux/amd64", "linux/arm64"}
	}
	var normalized []string
	for _, p := range c.Platforms {
		p = strings.TrimSpace(p)
		if p == "amd64" {
			normalized = append(normalized, "linux/amd64")
		} else if p == "arm64" {
			normalized = append(normalized, "linux/arm64")
		} else if p != "" {
			normalized = append(normalized, p)
		}
	}
	return normalized
}
