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

package docker

import (
	"fmt"
	"path/filepath"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
)

// Config represents the docker and buildx configuration loaded from .ap/docker.yaml.
type Config struct {
	Docker string `json:"docker"`
	Buildx string `json:"buildx"`
}

// DefaultConfig returns a Config with default docker and buildx versions from the table.
func DefaultConfig() *Config {
	var dockerVer, buildxVer string
	if rel := LatestDocker(); rel != nil {
		dockerVer = rel.Version
	}
	if rel := LatestBuildx(); rel != nil {
		buildxVer = rel.Version
	}
	return &Config{
		Docker: dockerVer,
		Buildx: buildxVer,
	}
}

// LoadConfig loads and validates the docker configuration from .ap/docker.yaml in the specified directory.
// If the configuration file does not exist, it returns nil, nil.
func LoadConfig(root string) (*Config, error) {
	configFile := filepath.Join(root, ".ap/docker.yaml")

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
	if c.Docker == "" || c.Docker == "latest" {
		if rel := LatestDocker(); rel != nil {
			c.Docker = rel.Version
		}
	}
	if c.Buildx == "" || c.Buildx == "latest" {
		if rel := LatestBuildx(); rel != nil {
			c.Buildx = rel.Version
		}
	}
	return nil
}
