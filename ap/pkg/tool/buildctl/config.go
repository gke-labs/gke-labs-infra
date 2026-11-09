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

package buildctl

import (
	"fmt"
	"path/filepath"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
)

// Config represents the buildctl configuration loaded strictly from .ap/buildctl.yaml.
type Config struct {
	Buildctl string `json:"buildctl"`
}

// DefaultConfig returns a Config with default buildctl version from the newest table entry.
func DefaultConfig() *Config {
	var buildctlVer string
	if rel := LatestBuildctl(); rel != nil {
		buildctlVer = rel.Version
	}
	return &Config{
		Buildctl: buildctlVer,
	}
}

// LoadConfig loads and validates the buildctl configuration from .ap/buildctl.yaml in the specified directory.
// If the configuration file does not exist, it returns nil, nil.
func LoadConfig(root string) (*Config, error) {
	configFile := filepath.Join(root, ".ap/buildctl.yaml")

	var cfg Config
	found, err := config.LoadYAML(configFile, &cfg)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}

	if err := cfg.ApplyDefaultsAndValidate(); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", configFile, err)
	}

	return &cfg, nil
}

// ApplyDefaultsAndValidate fills in defaults and validates configuration fields.
func (c *Config) ApplyDefaultsAndValidate() error {
	if c.Buildctl == "" || c.Buildctl == "latest" {
		if rel := LatestBuildctl(); rel != nil {
			c.Buildctl = rel.Version
		}
	}
	return nil
}
