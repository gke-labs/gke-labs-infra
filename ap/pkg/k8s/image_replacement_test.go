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

package k8s

import (
	"testing"
)

func TestReplacePlaceholderImages(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple image",
			input:    "image: example-server",
			expected: "image: ${IMAGE_PREFIX}/example-server:${IMAGE_TAG}",
		},
		{
			name:     "quoted image",
			input:    `image: "example-server"`,
			expected: `image: ${IMAGE_PREFIX}/example-server:${IMAGE_TAG}`,
		},
		{
			name:     "image with prefix already",
			input:    "image: gcr.io/example-server",
			expected: "image: gcr.io/example-server",
		},
		{
			name:     "image with tag already",
			input:    "image: example-server:v1",
			expected: "image: example-server:v1",
		},
		{
			name:     "image with both",
			input:    "image: gcr.io/example-server:v1",
			expected: "image: gcr.io/example-server:v1",
		},
		{
			name: "multiple images in manifest",
			input: `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: example
spec:
  template:
    spec:
      containers:
      - name: server
        image: example-server
      - name: sidecar
        image: "sidecar-image"
      - name: external
        image: gcr.io/other/image:latest
`,
			expected: `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: example
spec:
  template:
    spec:
      containers:
      - name: server
        image: ${IMAGE_PREFIX}/example-server:${IMAGE_TAG}
      - name: sidecar
        image: ${IMAGE_PREFIX}/sidecar-image:${IMAGE_TAG}
      - name: external
        image: gcr.io/other/image:latest
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := replacePlaceholderImages(tt.input)
			if got != tt.expected {
				t.Errorf("replacePlaceholderImages() = %v, want %v", got, tt.expected)
			}
		})
	}
}
