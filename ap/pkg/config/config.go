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
	Unused           *UnusedConfig           `json:"unused"`
	TestContext      *TestContextConfig      `json:"testcontext"`
	UnusedParameters *UnusedParametersConfig `json:"unusedparameters"`
}

type UnusedConfig struct {
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
