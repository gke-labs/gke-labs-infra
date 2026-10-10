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
	"runtime"
	"strings"
)

type Config struct {
	Gofmt       *GofmtConfig       `json:"gofmt"`
	Govet       *GovetConfig       `json:"govet"`
	Govulncheck *GovulncheckConfig `json:"govulncheck"`
	Skip        []string           `json:"skip"`
	Lint        *LintConfig        `json:"lint"`
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

type LintConfig struct {
	SkipGenerated                *bool                               `json:"skipGenerated"`
	SkipTests                    *bool                               `json:"skipTests"`
	Unused                       *UnusedConfig                       `json:"unused"`
	TestContext                  *TestContextConfig                  `json:"testcontext"`
	UnusedParameters             *UnusedParametersConfig             `json:"unusedparameters"`
	ReplaceEmptyInterfaceWithAny *ReplaceEmptyInterfaceWithAnyConfig `json:"replaceEmptyInterfaceWithAny"`
	DroppedErrors                *DroppedErrorsConfig                `json:"droppedErrors"`
}

type UnusedConfig struct {
	Enabled       *bool `json:"enabled"`
	SkipGenerated *bool `json:"skipGenerated"`
	SkipTests     *bool `json:"skipTests"`
}

type ReplaceEmptyInterfaceWithAnyConfig struct {
	Enabled       *bool `json:"enabled"`
	SkipGenerated *bool `json:"skipGenerated"`
	SkipTests     *bool `json:"skipTests"`
}

type TestContextConfig struct {
	Mode          string `json:"mode"`
	SkipGenerated *bool  `json:"skipGenerated"`
	SkipTests     *bool  `json:"skipTests"`
}

type UnusedParametersConfig struct {
	Mode string `json:"mode"`
}

type DroppedErrorsConfig struct {
	Mode               string   `json:"mode"`
	Exclude            []string `json:"exclude"`
	Baseline           string   `json:"baseline"`
	SkipTests          *bool    `json:"skipTests"`
	SkipGenerated      *bool    `json:"skipGenerated"`
	UseDefaultExcludes *bool    `json:"useDefaultExcludes"`
	GOOS               []string `json:"goos"`
}

// Load loads the configuration from .ap/go.yaml in the repository root.
func Load(repoRoot string) (*Config, error) {
	configFile := filepath.Join(repoRoot, ".ap/go.yaml")

	var config Config
	if _, err := os.Stat(configFile); err == nil {
		data, err := os.ReadFile(configFile)
		if err != nil {
			return nil, fmt.Errorf("error reading %s: %w", configFile, err)
		}

		if err := UnmarshalStrict(data, &config); err != nil {
			return nil, fmt.Errorf("error parsing %s: %w", configFile, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("error checking %s: %w", configFile, err)
	}

	if err := config.Validate(); err != nil {
		return nil, err
	}

	return &config, nil
}

// Validate checks the configuration for semantic errors.
func (c *Config) Validate() error {
	if c.Lint != nil && c.Lint.DroppedErrors != nil && c.Lint.DroppedErrors.Mode != "" {
		m := c.Lint.DroppedErrors.Mode
		if m != "error" && m != "warn" && m != "ignore" {
			return fmt.Errorf("invalid droppedErrors mode %q: must be one of 'error', 'warn', 'ignore'", m)
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

// IsUnusedEnabled returns true if unused detection is enabled in the config (defaulting to true).
func (c *Config) IsUnusedEnabled() bool {
	if c.Lint != nil && c.Lint.Unused != nil && c.Lint.Unused.Enabled != nil {
		return *c.Lint.Unused.Enabled
	}
	return true
}

// IsUnusedParametersEnabled returns true if unused parameter detection is enabled.
// Default is false.
func (c *Config) IsUnusedParametersEnabled() bool {
	if c.Lint != nil && c.Lint.UnusedParameters != nil {
		return c.Lint.UnusedParameters.Mode != "skip"
	}
	return false
}

// IsTestContextEnabled returns true if testcontext detection is enabled in the config (defaulting to true).
func (c *Config) IsTestContextEnabled() bool {
	if c.Lint != nil && c.Lint.TestContext != nil {
		return c.Lint.TestContext.Mode != "ignore"
	}
	return true
}

// IsTestContextError returns true if testcontext should be reported as an error.
// Default is false (warning).
func (c *Config) IsTestContextError() bool {
	if c.Lint != nil && c.Lint.TestContext != nil {
		return c.Lint.TestContext.Mode == "error"
	}
	return false
}

// IsReplaceEmptyInterfaceWithAnyEnabled returns true if the replace-empty-interface-with-any linter is enabled in the config (defaulting to true).
func (c *Config) IsReplaceEmptyInterfaceWithAnyEnabled() bool {
	if c.Lint != nil && c.Lint.ReplaceEmptyInterfaceWithAny != nil && c.Lint.ReplaceEmptyInterfaceWithAny.Enabled != nil {
		return *c.Lint.ReplaceEmptyInterfaceWithAny.Enabled
	}
	return true
}

// IsDroppedErrorsEnabled returns true if dropped error checking is enabled (defaulting to true, mode != "ignore").
func (c *Config) IsDroppedErrorsEnabled() bool {
	if c.Lint != nil && c.Lint.DroppedErrors != nil {
		return c.Lint.DroppedErrors.Mode != "ignore"
	}
	return true
}

// IsDroppedErrorsError returns true if dropped errors should be reported as an error.
// Default is true ("error" mode).
func (c *Config) IsDroppedErrorsError() bool {
	return c.DroppedErrorsMode() == "error"
}

// DroppedErrorsMode returns the mode for dropped error checking: "ignore", "warn", or "error".
// Default is "error".
func (c *Config) DroppedErrorsMode() string {
	if c.Lint != nil && c.Lint.DroppedErrors != nil && c.Lint.DroppedErrors.Mode != "" {
		return c.Lint.DroppedErrors.Mode
	}
	return "error"
}

// DroppedErrorsBaseline returns the configured baseline file path, or empty string.
func (c *Config) DroppedErrorsBaseline() string {
	if c.Lint != nil && c.Lint.DroppedErrors != nil {
		return c.Lint.DroppedErrors.Baseline
	}
	return ""
}

// DroppedErrorsExclude returns the configured excluded symbols.
func (c *Config) DroppedErrorsExclude() []string {
	if c.Lint != nil && c.Lint.DroppedErrors != nil {
		return c.Lint.DroppedErrors.Exclude
	}
	return nil
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
func (c *Config) ResolveSkipGenerated(override *bool, analyzerDefault bool) bool {
	var shared *bool
	if c != nil && c.Lint != nil {
		shared = c.Lint.SkipGenerated
	}
	return resolve(override, shared, analyzerDefault)
}

// ResolveSkipTests resolves whether test files should be skipped,
// checking a per-check override first, then lint.skipTests, then analyzerDefault.
func (c *Config) ResolveSkipTests(override *bool, analyzerDefault bool) bool {
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

// UnusedSkipGenerated returns the per-check override for unused skipGenerated, or nil.
func (c *Config) UnusedSkipGenerated() *bool {
	if c.Lint != nil && c.Lint.Unused != nil {
		return c.Lint.Unused.SkipGenerated
	}
	return nil
}

// UnusedSkipTests returns the per-check override for unused skipTests, or nil.
func (c *Config) UnusedSkipTests() *bool {
	if c.Lint != nil && c.Lint.Unused != nil {
		return c.Lint.Unused.SkipTests
	}
	return nil
}

// TestContextSkipGenerated returns the per-check override for testcontext skipGenerated, or nil.
func (c *Config) TestContextSkipGenerated() *bool {
	if c.Lint != nil && c.Lint.TestContext != nil {
		return c.Lint.TestContext.SkipGenerated
	}
	return nil
}

// TestContextSkipTests returns the per-check override for testcontext skipTests, or nil.
func (c *Config) TestContextSkipTests() *bool {
	if c.Lint != nil && c.Lint.TestContext != nil {
		return c.Lint.TestContext.SkipTests
	}
	return nil
}

// ReplaceEmptyInterfaceWithAnySkipGenerated returns the per-check override for replaceEmptyInterfaceWithAny skipGenerated, or nil.
func (c *Config) ReplaceEmptyInterfaceWithAnySkipGenerated() *bool {
	if c.Lint != nil && c.Lint.ReplaceEmptyInterfaceWithAny != nil {
		return c.Lint.ReplaceEmptyInterfaceWithAny.SkipGenerated
	}
	return nil
}

// ReplaceEmptyInterfaceWithAnySkipTests returns the per-check override for replaceEmptyInterfaceWithAny skipTests, or nil.
func (c *Config) ReplaceEmptyInterfaceWithAnySkipTests() *bool {
	if c.Lint != nil && c.Lint.ReplaceEmptyInterfaceWithAny != nil {
		return c.Lint.ReplaceEmptyInterfaceWithAny.SkipTests
	}
	return nil
}

// DroppedErrorsSkipTests returns true if test files should be skipped (default true).
func (c *Config) DroppedErrorsSkipTests() bool {
	var override *bool
	if c != nil && c.Lint != nil && c.Lint.DroppedErrors != nil {
		override = c.Lint.DroppedErrors.SkipTests
	}
	return c.ResolveSkipTests(override, true)
}

// DroppedErrorsSkipGenerated returns true if generated files should be skipped (default false).
func (c *Config) DroppedErrorsSkipGenerated() bool {
	var override *bool
	if c != nil && c.Lint != nil && c.Lint.DroppedErrors != nil {
		override = c.Lint.DroppedErrors.SkipGenerated
	}
	return c.ResolveSkipGenerated(override, false)
}

// DroppedErrorsUseDefaultExcludes returns true if default exclusions should be used (default true).
func (c *Config) DroppedErrorsUseDefaultExcludes() bool {
	if c.Lint != nil && c.Lint.DroppedErrors != nil {
		if c.Lint.DroppedErrors.UseDefaultExcludes != nil {
			return *c.Lint.DroppedErrors.UseDefaultExcludes
		}
	}
	return true
}

// DroppedErrorsGOOS returns the list of GOOS targets to check.
// Default is host plus "linux" (or just "linux" if host is linux).
func (c *Config) DroppedErrorsGOOS() []string {
	if c.Lint != nil && c.Lint.DroppedErrors != nil && len(c.Lint.DroppedErrors.GOOS) > 0 {
		return c.Lint.DroppedErrors.GOOS
	}
	host := runtime.GOOS
	if host == "linux" {
		return []string{"linux"}
	}
	return []string{host, "linux"}
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
	if _, err := os.Stat(configFile); err == nil {
		data, err := os.ReadFile(configFile)
		if err != nil {
			return nil, fmt.Errorf("error reading %s: %w", configFile, err)
		}

		if err := UnmarshalStrict(data, &config); err != nil {
			return nil, fmt.Errorf("error parsing %s: %w", configFile, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("error checking %s: %w", configFile, err)
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
	if _, err := os.Stat(configFile); err == nil {
		data, err := os.ReadFile(configFile)
		if err != nil {
			return nil, fmt.Errorf("error reading %s: %w", configFile, err)
		}

		if err := UnmarshalStrict(data, &config); err != nil {
			return nil, fmt.Errorf("error parsing %s: %w", configFile, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("error checking %s: %w", configFile, err)
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
