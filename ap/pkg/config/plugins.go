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

// IsEnabled returns true if the plugin is enabled (defaulting to true).
func (p PluginDeclaration) IsEnabled() bool {
	if p.Enabled == nil {
		return true
	}
	return *p.Enabled
}

// Validate checks the plugins configuration for semantic errors.
func (c *PluginsConfig) Validate() error {
	if c == nil {
		return nil
	}
	for name, decl := range c.Plugins {
		if !decl.IsEnabled() {
			continue
		}
		if (decl.Image == "" && decl.Dockerfile == "") || (decl.Image != "" && decl.Dockerfile != "") {
			return fmt.Errorf("plugin %q must specify either image or dockerfile", name)
		}
		if decl.Image != "" && !strings.Contains(decl.Image, "@sha256:") {
			return fmt.Errorf("plugin %q image %q must be pinned by digest (@sha256:...)", name, decl.Image)
		}
	}
	return nil
}

// LoadPluginsConfig loads plugin declarations from .ap/plugins.yaml or .ap/ap.yaml.
func LoadPluginsConfig(root string) (*PluginsConfig, error) {
	configFile := filepath.Join(root, ".ap", "plugins.yaml")
	var cfg PluginsConfig
	found, err := LoadYAML(configFile, &cfg)
	if err != nil {
		return nil, err
	}
	if !found {
		apCfg, err := LoadAPConfig(root)
		if err != nil || apCfg == nil || len(apCfg.Plugins) == 0 {
			return nil, err
		}
		cfg.Plugins = apCfg.Plugins
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", configFile, err)
	}
	return &cfg, nil
}
