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
)

// GenerateConfig represents generation and verification configuration.
type GenerateConfig struct {
	IgnoreOrphans []string `json:"ignoreOrphans"`
}

// PresubmitConfig represents presubmit configuration in ap.yaml.
type PresubmitConfig struct {
	SkipIfOnlyChanged []string `json:"skipIfOnlyChanged"`
}

// APConfig represents the configuration loaded from .ap/ap.yaml.
type APConfig struct {
	Version         string           `json:"version"`
	ImagePrefix     string           `json:"imagePrefix"`
	GeneratedHeader string           `json:"generatedHeader"`
	Presubmits      *PresubmitConfig `json:"presubmits"`
	Generate        *GenerateConfig  `json:"generate"`
}

// LoadAPConfig loads the configuration from .ap/ap.yaml using strict unmarshaling.
func LoadAPConfig(root string) (*APConfig, error) {
	configFile := filepath.Join(root, ".ap", "ap.yaml")
	data, err := os.ReadFile(configFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("error reading %s: %w", configFile, err)
	}

	var cfg APConfig
	if err := UnmarshalStrict(data, &cfg); err != nil {
		return nil, fmt.Errorf("error parsing %s: %w", configFile, err)
	}

	return &cfg, nil
}
