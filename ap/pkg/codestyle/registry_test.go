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

package codestyle

import (
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
)

func TestRegistry_FindAndDefaults(t *testing.T) {
	tests := []struct {
		name                 string
		lookup               string
		wantName             string
		wantDefaultMode      Mode
		wantDefaultSkipGen   bool
		wantDefaultSkipTests bool
	}{
		{
			name:                 "unused",
			lookup:               "unused",
			wantName:             "unused",
			wantDefaultMode:      ModeError,
			wantDefaultSkipGen:   true,
			wantDefaultSkipTests: false,
		},
		{
			name:                 "testcontext",
			lookup:               "testcontext",
			wantName:             "testcontext",
			wantDefaultMode:      ModeWarn,
			wantDefaultSkipGen:   false,
			wantDefaultSkipTests: false,
		},
		{
			name:                 "replaceEmptyInterfaceWithAny alias",
			lookup:               "replace-empty-interface-with-any",
			wantName:             "replaceEmptyInterfaceWithAny",
			wantDefaultMode:      ModeWarn,
			wantDefaultSkipGen:   true,
			wantDefaultSkipTests: false,
		},
		{
			name:                 "testonlyimports alias",
			lookup:               "testOnlyImports",
			wantName:             "testonlyimports",
			wantDefaultMode:      ModeError,
			wantDefaultSkipGen:   false,
			wantDefaultSkipTests: false,
		},
		{
			name:                 "droppederrors",
			lookup:               "droppedErrors",
			wantName:             "droppederrors",
			wantDefaultMode:      ModeError,
			wantDefaultSkipGen:   false,
			wantDefaultSkipTests: true,
		},
		{
			name:                 "closewriter alias",
			lookup:               "closeWriter",
			wantName:             "closewriter",
			wantDefaultMode:      ModeError,
			wantDefaultSkipGen:   false,
			wantDefaultSkipTests: false,
		},
		{
			name:                 "deferinloop alias",
			lookup:               "deferInLoop",
			wantName:             "deferinloop",
			wantDefaultMode:      ModeError,
			wantDefaultSkipGen:   false,
			wantDefaultSkipTests: false,
		},
		{
			name:                 "portability",
			lookup:               "portability",
			wantName:             "portability",
			wantDefaultMode:      ModeError,
			wantDefaultSkipGen:   false,
			wantDefaultSkipTests: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Find(tt.lookup)
			if r == nil {
				t.Fatalf("Find(%q) returned nil", tt.lookup)
			}
			if r.Name != tt.wantName {
				t.Errorf("r.Name = %q, want %q", r.Name, tt.wantName)
			}
			if r.DefaultMode != tt.wantDefaultMode {
				t.Errorf("r.DefaultMode = %v, want %v", r.DefaultMode, tt.wantDefaultMode)
			}
			if r.DefaultSkipGenerated != tt.wantDefaultSkipGen {
				t.Errorf("r.DefaultSkipGenerated = %v, want %v", r.DefaultSkipGenerated, tt.wantDefaultSkipGen)
			}
			if r.DefaultSkipTests != tt.wantDefaultSkipTests {
				t.Errorf("r.DefaultSkipTests = %v, want %v", r.DefaultSkipTests, tt.wantDefaultSkipTests)
			}
		})
	}
}

func TestRegistry_PlainAnalyzers(t *testing.T) {
	plain := PlainAnalyzers()
	if len(plain) != 7 {
		t.Fatalf("expected 7 plain analyzers, got %d", len(plain))
	}
}

func TestRegistry_ResolveWithConfig(t *testing.T) {
	yamlContent := `
lint:
  skipGenerated: true
  testcontext:
    mode: error
    skipTests: true
  unused:
    mode: ignore
    skipGenerated: false
`
	var cfg config.Config
	if err := config.UnmarshalStrict([]byte(yamlContent), &cfg); err != nil {
		t.Fatalf("failed to unmarshal config: %v", err)
	}

	tc := Find("testcontext")
	if tc.ResolveMode(&cfg) != ModeError {
		t.Errorf("expected testcontext mode error, got %v", tc.ResolveMode(&cfg))
	}
	if !tc.ResolveSkipGenerated(&cfg) {
		t.Errorf("expected testcontext skipGenerated true (from shared)")
	}
	if !tc.ResolveSkipTests(&cfg) {
		t.Errorf("expected testcontext skipTests true (from override)")
	}

	u := Find("unused")
	if u.ResolveMode(&cfg) != ModeIgnore {
		t.Errorf("expected unused mode ignore, got %v", u.ResolveMode(&cfg))
	}
	if u.ResolveSkipGenerated(&cfg) {
		t.Errorf("expected unused skipGenerated false (from override)")
	}
}
