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
	"path/filepath"
	"strings"

	"golang.org/x/mod/semver"
)

// GenerateConfig represents generation and verification configuration.
type GenerateConfig struct {
	IgnoreOrphans  []string `json:"ignoreOrphans"`
	AuditPaths     []string `json:"auditPaths"`
	AuditGenerated bool     `json:"auditGenerated"`
}

// PresubmitConfig represents presubmit configuration in ap.yaml.
type PresubmitConfig struct {
	SkipIfOnlyChanged []string `json:"skipIfOnlyChanged"`
}

// APConfig represents the configuration loaded from .ap/ap.yaml.
type APConfig struct {
	Version     string           `json:"version"`
	ImagePrefix string           `json:"imagePrefix"`
	Presubmits  *PresubmitConfig `json:"presubmits"`
	Generate    *GenerateConfig  `json:"generate"`
}

// LoadAPConfig loads the configuration from .ap/ap.yaml using strict unmarshaling.
func LoadAPConfig(root string) (*APConfig, error) {
	configFile := filepath.Join(root, ".ap", "ap.yaml")
	var cfg APConfig
	found, err := loadYAML(configFile, &cfg)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Validate checks the configuration for semantic errors.
func (c *APConfig) Validate() error {
	if c == nil {
		return nil
	}

	if c.Version != "" && c.Version != "!self" && c.Version != "latest" && !semver.IsValid(c.Version) {
		return fmt.Errorf("invalid version %q: must be '!self', 'latest', or a module version string", c.Version)
	}

	if c.Presubmits != nil {
		for _, p := range c.Presubmits.SkipIfOnlyChanged {
			if !presubmitSkipPatternOK(p) {
				return fmt.Errorf("presubmits.skipIfOnlyChanged pattern %q: only letters, digits, and / . _ - * ? ! [ ] are allowed", p)
			}
		}
	}

	return nil
}

// presubmitSkipPatternOK reports whether a skipIfOnlyChanged pattern
// is safe to splice into the generated bash `case` statement.
func presubmitSkipPatternOK(p string) bool {
	if p == "" {
		return false
	}
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("/._-*?![]", r):
		default:
			return false
		}
	}
	return true
}
