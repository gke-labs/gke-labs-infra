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
	"reflect"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestRepositoryConfig_YAML(t *testing.T) {
	desc := "test description"
	priv := true
	
	cfg := RepositoryConfig{
		Owner:       "test-owner",
		Name:        "test-repo",
		Description: &desc,
		Private:     &priv,
		Topics:      []string{"go", "k8s"},
		Settings: &RepositorySettings{
			AllowAutoMerge:   boolPtr(true),
			AllowSquashMerge: boolPtr(false),
		},
		BranchProtection: map[string]*BranchProtection{
			"main": {
				EnforceAdmins: true,
				RequiredStatusChecks: &RequiredStatusChecks{
					Strict:   true,
					Contexts: []string{"test"},
				},
			},
		},
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var parsed RepositoryConfig
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if !reflect.DeepEqual(cfg, parsed) {
		t.Errorf("Roundtrip failed.\nOriginal: %+v\nParsed:   %+v", cfg, parsed)
	}
}

func boolPtr(b bool) *bool {
	return &b
}
