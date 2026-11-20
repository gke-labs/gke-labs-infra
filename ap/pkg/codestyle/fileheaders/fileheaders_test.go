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

package fileheaders

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gke-labs/gke-labs-infra/ap/pkg/config"
	"github.com/gke-labs/gke-labs-infra/ap/pkg/sourcetree"
)

func runTest(ctx context.Context, repoRoot string, files []string) error {
	r, err := sourcetree.Load(ctx, repoRoot)
	if err != nil {
		return err
	}
	return Run(ctx, r, repoRoot, files)
}

func TestRun_Skip(t *testing.T) {
	tmpDir := t.TempDir()

	// Create config
	configDir := filepath.Join(tmpDir, ".ap")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(configDir, "headers.yaml")
	configContent := `
license: apache-2.0
copyrightHolder: Google LLC
skip:
- "*.yaml"
`
	if err := os.WriteFile(configFile, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a yaml file that should be skipped
	subDir := filepath.Join(tmpDir, "foo/bar")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	targetFile := filepath.Join(subDir, "file.yaml")
	fileContent := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test
`
	if err := os.WriteFile(targetFile, []byte(fileContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a go file that should get a header
	goFile := filepath.Join(subDir, "test.go")
	goContent := `package bar`
	if err := os.WriteFile(goFile, []byte(goContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Run fileheaders
	ctx := t.Context()
	if err := runTest(ctx, tmpDir, nil); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Verify yaml file was NOT modified
	content, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != fileContent {
		t.Errorf("YAML file was modified but should have been skipped. Content:\n%s", string(content))
	}

	// Verify go file WAS modified
	content, err = os.ReadFile(goFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "Copyright") {
		t.Errorf("Go file was NOT modified but should have been. Content:\n%s", string(content))
	}
}

func TestRun_Skip_ExplicitFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create config
	configDir := filepath.Join(tmpDir, ".ap")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(configDir, "headers.yaml")
	configContent := `
license: apache-2.0
copyrightHolder: Google LLC
skip:
- "*.yaml"
`
	if err := os.WriteFile(configFile, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a yaml file that should be skipped
	subDir := filepath.Join(tmpDir, "foo/bar")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	targetFile := filepath.Join(subDir, "file.yaml")
	fileContent := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test
`
	if err := os.WriteFile(targetFile, []byte(fileContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Run fileheaders with explicit file
	ctx := t.Context()
	if err := runTest(ctx, tmpDir, []string{targetFile}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Verify yaml file was NOT modified
	content, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != fileContent {
		t.Errorf("YAML file was modified but should have been skipped. Content:\n%s", string(content))
	}
}

func TestRun_KubernetesStyle(t *testing.T) {
	tmpDir := t.TempDir()

	// Create config
	configDir := filepath.Join(tmpDir, ".ap")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(configDir, "headers.yaml")
	configContent := `
license: apache-2.0
copyrightHolder: Google LLC
`
	if err := os.WriteFile(configFile, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a go file with K8s style header
	targetFile := filepath.Join(tmpDir, "k8s.go")
	fileContent := `/*
	Copyright 2018 The Kubernetes Authors.

	Licensed under the Apache License, Version 2.0 (the "License");
	you may not use this file except in compliance with the License.
	You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

	Unless required by applicable law or agreed to in writing, software
	distributed under the License is distributed on an "AS IS" BASIS,
	WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
	See the License for the specific language governing permissions and
	limitations under the License.
	*/

	package main
	`
	if err := os.WriteFile(targetFile, []byte(fileContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Run fileheaders
	ctx := t.Context()
	if err := runTest(ctx, tmpDir, []string{targetFile}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Verify file was NOT modified
	content, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != fileContent {
		t.Errorf("File was modified but should have been skipped. Content:\n%s", string(content))
	}
}

func TestRun_FixCorruptedHeader(t *testing.T) {
	tmpDir := t.TempDir()

	// Create config
	configDir := filepath.Join(tmpDir, ".ap")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(configDir, "headers.yaml")
	configContent := `license: apache-2.0
copyrightHolder: Google LLC
`
	if err := os.WriteFile(configFile, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		filename    string
		initContent string
	}{
		{
			name:     "corrupted url",
			filename: "corrupted_url.go",
			initContent: `// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.01
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

func main() {}
`,
		},
		{
			name:     "spelling error in license name",
			filename: "spelling_error.go",
			initContent: `// Copyright 2026 Google LLC
//
// Licensed under the Apashe License, Version 2.0 (the "License");
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

package main

func main() {}
`,
		},
		{
			name:     "missing a license line",
			filename: "missing_line.go",
			initContent: `// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

func main() {}
`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			targetFile := filepath.Join(tmpDir, tc.filename)
			if err := os.WriteFile(targetFile, []byte(tc.initContent), 0644); err != nil {
				t.Fatal(err)
			}

			// Run fileheaders
			ctx := t.Context()
			if err := runTest(ctx, tmpDir, []string{targetFile}); err != nil {
				t.Fatalf("Run failed: %v", err)
			}

			// Verify file was modified to have the correct header
			content, err := os.ReadFile(targetFile)
			if err != nil {
				t.Fatal(err)
			}

			contentStr := string(content)
			if !strings.Contains(contentStr, "Apache License, Version 2.0") {
				t.Errorf("File did not contain valid Apache license header. Content:\n%s", contentStr)
			}
			if strings.Contains(contentStr, "http://www.apache.org/licenses/LICENSE-2.01") {
				t.Errorf("File still contained corrupted URL. Content:\n%s", contentStr)
			}
			if strings.Contains(contentStr, "Apashe") {
				t.Errorf("File still contained spelling error 'Apashe'. Content:\n%s", contentStr)
			}
			if !strings.Contains(contentStr, "package main") {
				t.Errorf("File code (package main) was lost! Content:\n%s", contentStr)
			}
		})
	}
}

func TestRun_LicenseInMiddleOfStringOrDoc(t *testing.T) {
	tmpDir := t.TempDir()

	// Create config
	configDir := filepath.Join(tmpDir, ".ap")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(configDir, "headers.yaml")
	configContent := `license: apache-2.0
copyrightHolder: Google LLC
`
	if err := os.WriteFile(configFile, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	targetFile := filepath.Join(tmpDir, "string_lit.go")
	// This file lacks a copyright header at the top but has one inside a string literal in the middle.
	fileContent := `package main

const licenseText = ` + "`" + `// Copyright 2026 Google LLC
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
` + "`" + `

func main() {}
`
	if err := os.WriteFile(targetFile, []byte(fileContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Run fileheaders
	ctx := t.Context()
	if err := runTest(ctx, tmpDir, []string{targetFile}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Verify file WAS modified to add the correct header at the top, even though there was a license block in the string literal.
	content, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatal(err)
	}

	contentStr := string(content)
	// The top of the file should now contain the license block
	if !strings.HasPrefix(contentStr, "// Copyright") {
		t.Errorf("Expected the license header to be added at the very top of the file. Content:\n%s", contentStr)
	}
}

func TestGenerateHeader(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *config.HeadersConfig
		want    string
		wantErr bool
	}{
		{
			name: "nil config",
			cfg:  nil,
			want: "",
		},
		{
			name: "empty license",
			cfg:  &config.HeadersConfig{},
			want: "",
		},
		{
			name: "license none",
			cfg:  &config.HeadersConfig{License: "none"},
			want: "",
		},
		{
			name: "apache-2.0 license",
			cfg:  &config.HeadersConfig{License: "apache-2.0", CopyrightHolder: "Test Org"},
			want: "// Copyright",
		},
		{
			name:    "unsupported license",
			cfg:     &config.HeadersConfig{License: "mit"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := GenerateHeader(tc.cfg, "//")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %s, got nil", tc.name)
				}
				if !strings.Contains(err.Error(), ".ap/headers.yaml") {
					t.Errorf("expected error to mention .ap/headers.yaml, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %s: %v", tc.name, err)
			}
			if tc.want == "" && got != "" {
				t.Errorf("expected empty header, got %q", got)
			}
			if tc.want != "" && !strings.HasPrefix(got, tc.want) {
				t.Errorf("expected prefix %q, got %q", tc.want, got)
			}
		})
	}
}

func TestRun_LicenseNone(t *testing.T) {
	tmpDir := t.TempDir()

	configDir := filepath.Join(tmpDir, ".ap")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(configDir, "headers.yaml")
	if err := os.WriteFile(configFile, []byte("license: none\n"), 0644); err != nil {
		t.Fatal(err)
	}

	targetFile := filepath.Join(tmpDir, "main.go")
	originalContent := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(targetFile, []byte(originalContent), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	if err := runTest(ctx, tmpDir, []string{targetFile}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	content, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != originalContent {
		t.Errorf("Expected content unchanged when license is none, got:\n%s", string(content))
	}
}

func TestRun_UnsupportedLicense(t *testing.T) {
	tmpDir := t.TempDir()

	configDir := filepath.Join(tmpDir, ".ap")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(configDir, "headers.yaml")
	if err := os.WriteFile(configFile, []byte("license: mit\n"), 0644); err != nil {
		t.Fatal(err)
	}

	targetFile := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(targetFile, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	err := runTest(ctx, tmpDir, []string{targetFile})
	if err == nil {
		t.Fatal("Expected error for unsupported license, got nil")
	}
	if !strings.Contains(err.Error(), ".ap/headers.yaml") {
		t.Errorf("Expected error to mention .ap/headers.yaml, got %v", err)
	}
}

func TestHasK8sStyleBlockHeader(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "k8s standard block header",
			input:    "/*\nCopyright 2018 The Kubernetes Authors.\n*/\npackage main\n",
			expected: true,
		},
		{
			name:     "inline block comment with Copyright",
			input:    "/* Copyright 2026 */\n",
			expected: true,
		},
		{
			name:     "line comment with Copyright",
			input:    "// Copyright 2026 Google LLC\n",
			expected: false,
		},
		{
			name:     "block comment without Copyright",
			input:    "/* Some other comment */\n",
			expected: false,
		},
		{
			name:     "Copyright before block comment start",
			input:    "Copyright /* not a header */\n",
			expected: false,
		},
		{
			name:     "empty",
			input:    "",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasK8sStyleBlockHeader([]byte(tt.input))
			if got != tt.expected {
				t.Errorf("hasK8sStyleBlockHeader(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestMatchExpectedHeader(t *testing.T) {
	validGoHeader := `// Copyright 2026 Google LLC
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
// limitations under the License.`

	validHashHeader := `# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.`

	tests := []struct {
		name     string
		header   string
		style    string
		holder   string
		expected bool
	}{
		{
			name:     "valid Go header",
			header:   validGoHeader,
			style:    "//",
			holder:   "Google LLC",
			expected: true,
		},
		{
			name:     "valid hash header",
			header:   validHashHeader,
			style:    "#",
			holder:   "Google LLC",
			expected: true,
		},
		{
			name:     "valid with year range",
			header:   strings.Replace(validGoHeader, "2026", "2020-2026", 1),
			style:    "//",
			holder:   "Google LLC",
			expected: true,
		},
		{
			name:     "valid with year list and range",
			header:   strings.Replace(validGoHeader, "2026", "2020, 2022-2026", 1),
			style:    "//",
			holder:   "Google LLC",
			expected: true,
		},
		{
			name:     "valid with You capitalized",
			header:   strings.Replace(validGoHeader, "you may not use", "You may not use", 1),
			style:    "//",
			holder:   "Google LLC",
			expected: true,
		},
		{
			name:     "valid with you lowercase on obtain line",
			header:   strings.Replace(validGoHeader, "You may obtain", "you may obtain", 1),
			style:    "//",
			holder:   "Google LLC",
			expected: true,
		},
		{
			name:     "valid with empty copyright holder",
			header:   strings.Replace(validGoHeader, "Copyright 2026 Google LLC", "Copyright 2026", 1),
			style:    "//",
			holder:   "",
			expected: true,
		},
		{
			name:     "extra comment line before license block (e.g. go:build)",
			header:   "//go:build linux\n" + validGoHeader,
			style:    "//",
			holder:   "Google LLC",
			expected: true,
		},
		{
			name:     "extra comment line after license block (e.g. package doc)",
			header:   validGoHeader + "\n// Package foo does things.",
			style:    "//",
			holder:   "Google LLC",
			expected: true,
		},
		{
			name:     "wrong holder",
			header:   validGoHeader,
			style:    "//",
			holder:   "Other Org",
			expected: false,
		},
		{
			name:     "wrong comment style",
			header:   validGoHeader,
			style:    "#",
			holder:   "Google LLC",
			expected: false,
		},
		{
			name:     "corrupted url",
			header:   strings.Replace(validGoHeader, "LICENSE-2.0", "LICENSE-2.01", 1),
			style:    "//",
			holder:   "Google LLC",
			expected: false,
		},
		{
			name:     "missing line",
			header:   strings.Replace(validGoHeader, "//     http://www.apache.org/licenses/LICENSE-2.0\n", "", 1),
			style:    "//",
			holder:   "Google LLC",
			expected: false,
		},
		{
			name:     "invalid year characters",
			header:   strings.Replace(validGoHeader, "2026", "2026abc", 1),
			style:    "//",
			holder:   "Google LLC",
			expected: false,
		},
		{
			name:     "license text without comment markers",
			header:   strings.ReplaceAll(validGoHeader, "// ", ""),
			style:    "//",
			holder:   "Google LLC",
			expected: false,
		},
		{
			name:     "missing comment marker on internal line",
			header:   strings.Replace(validGoHeader, "// you may not use", "you may not use", 1),
			style:    "//",
			holder:   "Google LLC",
			expected: false,
		},
		{
			name:     "empty",
			header:   "",
			style:    "//",
			holder:   "Google LLC",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchExpectedHeader(tt.header, tt.style, tt.holder)
			if got != tt.expected {
				t.Errorf("matchExpectedHeader() = %v, want %v", got, tt.expected)
			}
		})
	}
}
