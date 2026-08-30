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

	"sigs.k8s.io/yaml"
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
	Unused                       *UnusedConfig                       `json:"unused"`
	TestContext                  *TestContextConfig                  `json:"testcontext"`
	UnusedParameters             *UnusedParametersConfig             `json:"unusedparameters"`
	ReplaceEmptyInterfaceWithAny *ReplaceEmptyInterfaceWithAnyConfig `json:"replaceEmptyInterfaceWithAny"`
}

type UnusedConfig struct {
	Enabled *bool `json:"enabled"`
}

type ReplaceEmptyInterfaceWithAnyConfig struct {
	Enabled *bool `json:"enabled"`
}

type TestContextConfig struct {
	Mode string `json:"mode"`
}

type UnusedParametersConfig struct {
	Mode string `json:"mode"`
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

		if err := yaml.Unmarshal(data, &config); err != nil {
			return nil, fmt.Errorf("error parsing %s: %w", configFile, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("error checking %s: %w", configFile, err)
	}

	return &config, nil
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

		if err := yaml.Unmarshal(data, &config); err != nil {
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

		if err := yaml.Unmarshal(data, &config); err != nil {
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
