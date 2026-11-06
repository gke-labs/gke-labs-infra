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

package protoc

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
)

const (
	// DefaultProtocVersion is the default protoc version resolution.
	DefaultProtocVersion = "latest"
)

// Config represents the protobuf configuration loaded from .ap/proto.yaml.
type Config struct {
	Protoc  string            `json:"protoc"`
	Plugins map[string]string `json:"plugins"`
}

// DefaultConfig returns a Config with default protoc and plugin versions.
func DefaultConfig() *Config {
	return &Config{
		Protoc: DefaultProtocVersion,
	}
}

// LoadConfig loads and validates the protobuf configuration from .ap/proto.yaml in the specified directory.
// If the configuration file does not exist, it returns nil, nil.
func LoadConfig(root string) (*Config, error) {
	configFile := filepath.Join(root, ".ap/proto.yaml")

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
	if c.Protoc == "" {
		c.Protoc = DefaultProtocVersion
	}
	if c.Protoc != "latest" && !strings.Contains(c.Protoc, ".") {
		return fmt.Errorf("invalid protoc version: %q", c.Protoc)
	}

	if c.Plugins != nil {
		for name, ver := range c.Plugins {
			if ver == "" {
				return fmt.Errorf("plugin %q has empty version", name)
			}
			if ver != "latest" && !strings.HasPrefix(ver, "v") && !strings.Contains(ver, "/") {
				c.Plugins[name] = "v" + ver
			}
		}
	}

	return nil
}
