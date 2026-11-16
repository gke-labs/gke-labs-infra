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
)

// PluginsConfig represents plugin declarations loaded from .ap/plugins.yaml.
type PluginsConfig struct {
	Plugins map[string]PluginDeclaration `json:"plugins"`
}

// PluginDeclaration represents an individual plugin declaration.
type PluginDeclaration struct {
	Image      string `json:"image,omitempty"`
	Dockerfile string `json:"dockerfile,omitempty"`
	Network    bool   `json:"network,omitempty"`
	Enabled    *bool  `json:"enabled,omitempty"`
}

// IsEnabled reports whether the plugin is enabled (defaulting to true).
func (declaration PluginDeclaration) IsEnabled() bool {
	if declaration.Enabled == nil {
		return true
	}
	return *declaration.Enabled
}

// Validate checks the plugins configuration for semantic errors.
func (config *PluginsConfig) Validate() error {
	for name, declaration := range config.Plugins {
		if !declaration.IsEnabled() {
			continue
		}
		if declaration.Image != "" && declaration.Dockerfile != "" {
			return fmt.Errorf("plugin %q: cannot specify both image and dockerfile", name)
		}
		if declaration.Image == "" && declaration.Dockerfile == "" {
			return fmt.Errorf("plugin %q: must specify either image or dockerfile", name)
		}
		if declaration.Image != "" && !strings.Contains(declaration.Image, "@sha256:") {
			return fmt.Errorf("plugin %q image %q must be pinned by digest (@sha256:...)", name, declaration.Image)
		}
	}
	return nil
}

// LoadPluginsConfig loads plugin declarations from .ap/plugins.yaml.
func LoadPluginsConfig(root string) (*PluginsConfig, error) {
	configFile := filepath.Join(root, ".ap", "plugins.yaml")
	var config PluginsConfig
	found, err := LoadYAML(configFile, &config)
	if err != nil {
		return nil, err
	}
	if !found {
		return &PluginsConfig{}, nil
	}
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", configFile, err)
	}
	return &config, nil
}
