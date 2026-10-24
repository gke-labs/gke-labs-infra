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
	"reflect"
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
			expected: "image: my-repo/example-server:v1",
		},
		{
			name:     "quoted image",
			input:    `image: "example-server"`,
			expected: `image: my-repo/example-server:v1`,
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
        image: my-repo/example-server:v1
      - name: sidecar
        image: my-repo/sidecar-image:v1
      - name: external
        image: gcr.io/other/image:latest
`,
		},
		{
			name: "multi-document YAML",
			input: `
apiVersion: v1
kind: Service
metadata:
  name: svc
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dep
spec:
  template:
    spec:
      containers:
      - name: main
        image: main-image
`,
			expected: `
apiVersion: v1
kind: Service
metadata:
  name: svc
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dep
spec:
  template:
    spec:
      containers:
      - name: main
        image: my-repo/main-image:v1
`,
		},
		{
			name: "comments and formatting",
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
        image: example-server # This is a placeholder
        # Some comment
      - name: sidecar
        image: "sidecar-image"  # Another placeholder
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
        image: my-repo/example-server:v1 # This is a placeholder
        # Some comment
      - name: sidecar
        image: my-repo/sidecar-image:v1  # Another placeholder
`,
		},
		{
			name: "image in non-container field",
			input: `
metadata:
  labels:
    image: label-image
`,
			expected: `
metadata:
  labels:
    image: label-image
`,
		},
		{
			name:     "image with latest tag",
			input:    "image: example-server:latest",
			expected: "image: my-repo/example-server:v1",
		},
		{
			name:     "image with path and latest tag",
			input:    "image: bar/foo:latest",
			expected: "image: my-repo/bar/foo:v1",
		},
		{
			name:     "image with host should be skipped",
			input:    "image: registry.k8s.io/kube-apiserver:latest",
			expected: "image: registry.k8s.io/kube-apiserver:latest",
		},
		{
			name:     "image with path no tag",
			input:    "image: bar/foo",
			expected: "image: my-repo/bar/foo:v1",
		},
		{
			name: "env value with implied tag",
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
        image: gcr.io/fixed/image:v1
        env:
        - name: HELPER_IMAGE
          value: ${IMAGE_PREFIX}/helper
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
        image: gcr.io/fixed/image:v1
        env:
        - name: HELPER_IMAGE
          value: my-repo/helper:v1
`,
		},
		{
			name: "env value with spelled-out tag",
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
        image: gcr.io/fixed/image:v1
        env:
        - name: HELPER_IMAGE
          value: ${IMAGE_PREFIX}/helper:${IMAGE_TAG}
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
        image: gcr.io/fixed/image:v1
        env:
        - name: HELPER_IMAGE
          value: my-repo/helper:v1
`,
		},
		{
			name: "quoted env value",
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
        image: gcr.io/fixed/image:v1
        env:
        - name: HELPER_IMAGE
          value: "${IMAGE_PREFIX}/helper"
        - name: SPELLED_IMAGE
          value: "${IMAGE_PREFIX}/spelled:${IMAGE_TAG}"
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
        image: gcr.io/fixed/image:v1
        env:
        - name: HELPER_IMAGE
          value: "my-repo/helper:v1"
        - name: SPELLED_IMAGE
          value: "my-repo/spelled:v1"
`,
		},
		{
			name: "container args whole-value both forms",
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
        image: gcr.io/fixed/image:v1
        args:
        - ${IMAGE_PREFIX}/worker
        - ${IMAGE_PREFIX}/worker-spelled:${IMAGE_TAG}
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
        image: gcr.io/fixed/image:v1
        args:
        - my-repo/worker:v1
        - my-repo/worker-spelled:v1
`,
		},
		{
			name: "container args embedded in --flag= form both forms",
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
        image: gcr.io/fixed/image:v1
        args:
        - --worker-image=${IMAGE_PREFIX}/worker
        - --spelled-image=${IMAGE_PREFIX}/worker-spelled:${IMAGE_TAG}
        - "--quoted-flag=${IMAGE_PREFIX}/quoted"
        - '--single-quoted=${IMAGE_PREFIX}/single:${IMAGE_TAG}'
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
        image: gcr.io/fixed/image:v1
        args:
        - --worker-image=my-repo/worker:v1
        - --spelled-image=my-repo/worker-spelled:v1
        - "--quoted-flag=my-repo/quoted:v1"
        - '--single-quoted=my-repo/single:v1'
`,
		},
		{
			name: "ConfigMap data single values and multiline block",
			input: `
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-config
data:
  image: ${IMAGE_PREFIX}/cm-image
  image_spelled: ${IMAGE_PREFIX}/cm-spelled:${IMAGE_TAG}
  app.conf: |
    server.image = "${IMAGE_PREFIX}/server"
    worker.image = "${IMAGE_PREFIX}/worker:${IMAGE_TAG}"
    other = "$FOO and ${OTHER}"
`,
			expected: `
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-config
data:
  image: my-repo/cm-image:v1
  image_spelled: my-repo/cm-spelled:v1
  app.conf: |
    server.image = "my-repo/server:v1"
    worker.image = "my-repo/worker:v1"
    other = "$FOO and ${OTHER}"
`,
		},
		{
			name: "strings that merely contain $ or ${OTHER} are left unchanged",
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
        image: gcr.io/fixed/image:v1
        env:
        - name: PRICE
          value: "$100"
        - name: OTHER_VAR
          value: "${OTHER}"
        - name: ESCAPED
          value: "$$NOT_A_VAR"
        - name: PREFIX_ALONE
          value: "${IMAGE_PREFIX}"
        - name: TAG_ALONE
          value: "${IMAGE_TAG}"
        - name: INVALID_TAG
          value: "${IMAGE_PREFIX}/foo:latest"
        - name: UNKNOWN_VAR_TAG
          value: "${IMAGE_PREFIX}/foo:${OTHER}"
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
        image: gcr.io/fixed/image:v1
        env:
        - name: PRICE
          value: "$100"
        - name: OTHER_VAR
          value: "${OTHER}"
        - name: ESCAPED
          value: "$$NOT_A_VAR"
        - name: PREFIX_ALONE
          value: "${IMAGE_PREFIX}"
        - name: TAG_ALONE
          value: "${IMAGE_TAG}"
        - name: INVALID_TAG
          value: "${IMAGE_PREFIX}/foo:latest"
        - name: UNKNOWN_VAR_TAG
          value: "${IMAGE_PREFIX}/foo:${OTHER}"
`,
		},
		{
			name: "image field with ${IMAGE_PREFIX}/<name>",
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
        image: ${IMAGE_PREFIX}/example-server
      - name: sidecar
        image: "${IMAGE_PREFIX}/sidecar:${IMAGE_TAG}"
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
        image: my-repo/example-server:v1
      - name: sidecar
        image: "my-repo/sidecar:v1"
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := replacePlaceholderImages(tt.input, "my-repo", "v1")
			if err != nil {
				t.Fatalf("replacePlaceholderImages() error = %v", err)
			}
			if got != tt.expected {
				t.Errorf("replacePlaceholderImages() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestFindPlaceholderImages(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "empty",
			input:    "",
			expected: nil,
		},
		{
			name:     "simple placeholder",
			input:    "image: my-app",
			expected: []string{"my-app"},
		},
		{
			name:     "placeholder with latest tag",
			input:    "image: my-app:latest",
			expected: []string{"my-app"},
		},
		{
			name:     "placeholder with path",
			input:    "image: sub/app",
			expected: []string{"sub/app"},
		},
		{
			name:     "external image with domain",
			input:    "image: gcr.io/my-project/my-app:latest",
			expected: nil,
		},
		{
			name:     "pinned version tag",
			input:    "image: redis:7.0",
			expected: nil,
		},
		{
			name: "multi-document deployment and service",
			input: `
apiVersion: v1
kind: Service
metadata:
  name: svc
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dep
spec:
  template:
    spec:
      containers:
      - name: app
        image: app-server
      - name: sidecar
        image: sidecar-proxy
      - name: external
        image: registry.k8s.io/pause:3.9
`,
			expected: []string{"app-server", "sidecar-proxy"},
		},
		{
			name: "duplicate references deduplicated and sorted",
			input: `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dep
spec:
  template:
    spec:
      initContainers:
      - name: init
        image: app-server
      containers:
      - name: app
        image: app-server
      - name: sidecar
        image: a-sidecar
`,
			expected: []string{"a-sidecar", "app-server"},
		},
		{
			name: "references in env and container args",
			input: `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dep
spec:
  template:
    spec:
      containers:
      - name: app
        image: gcr.io/fixed/base:v1
        args:
        - --worker-image=${IMAGE_PREFIX}/worker-app:${IMAGE_TAG}
        env:
        - name: HELPER
          value: ${IMAGE_PREFIX}/helper-app
`,
			expected: []string{"helper-app", "worker-app"},
		},
		{
			name: "references in ConfigMap data",
			input: `
apiVersion: v1
kind: ConfigMap
metadata:
  name: cfg
data:
  image: ${IMAGE_PREFIX}/cm-service
  app.conf: |
    proxy = "${IMAGE_PREFIX}/proxy-service:${IMAGE_TAG}"
`,
			expected: []string{"cm-service", "proxy-service"},
		},
		{
			name: "strings with $ or ${OTHER} not treated as placeholder images",
			input: `
apiVersion: v1
kind: ConfigMap
metadata:
  name: cfg
data:
  price: "$100"
  var1: "${OTHER}"
  var2: "${IMAGE_PREFIX}"
  var3: "${IMAGE_TAG}"
  invalid_tag: "${IMAGE_PREFIX}/foo:latest"
  unknown_tag: "${IMAGE_PREFIX}/foo:${OTHER}"
`,
			expected: nil,
		},
		{
			name: "image field and string references combined and deduplicated",
			input: `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dep
spec:
  template:
    spec:
      containers:
      - name: app
        image: my-service
        args:
        - --self=${IMAGE_PREFIX}/my-service
        - --sidecar=${IMAGE_PREFIX}/my-sidecar:${IMAGE_TAG}
`,
			expected: []string{"my-service", "my-sidecar"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FindPlaceholderImages(tt.input)
			if err != nil {
				t.Fatalf("FindPlaceholderImages() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("FindPlaceholderImages() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestReplacePlaceholderImages_ReferenceRewriting(t *testing.T) {
	manifestWithMix := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mixed-app
spec:
  template:
    spec:
      containers:
      - name: app
        image: app
        env:
        - name: APP_IMAGE
          value: ${IMAGE_PREFIX}/app
      - name: sidecar
        image: gcr.io/external/sidecar:v1.2.3
      - name: proxy
        image: docker.io/library/nginx:1.25-alpine
`

	t.Run("tag (unique IMAGE_TAG leaves tag intact without digest)", func(t *testing.T) {
		digests := map[string]string{
			"app": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}
		got, err := replacePlaceholderImages(manifestWithMix, "example.com/repo", "v1.0", digests)
		if err != nil {
			t.Fatalf("replacePlaceholderImages failed: %v", err)
		}

		expected := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mixed-app
spec:
  template:
    spec:
      containers:
      - name: app
        image: example.com/repo/app:v1.0
        env:
        - name: APP_IMAGE
          value: example.com/repo/app:v1.0
      - name: sidecar
        image: gcr.io/external/sidecar:v1.2.3
      - name: proxy
        image: docker.io/library/nginx:1.25-alpine
`
		if got != expected {
			t.Errorf("replacePlaceholderImages mismatch (-got +want):\nGOT:\n%s\nWANT:\n%s", got, expected)
		}
	})

	t.Run("tag@digest (default/latest tag rewrites to tag@sha256:digest)", func(t *testing.T) {
		digests := map[string]string{
			"app": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		}
		got, err := replacePlaceholderImages(manifestWithMix, "example.com/repo", "latest", digests)
		if err != nil {
			t.Fatalf("replacePlaceholderImages failed: %v", err)
		}

		expected := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mixed-app
spec:
  template:
    spec:
      containers:
      - name: app
        image: example.com/repo/app:latest@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
        env:
        - name: APP_IMAGE
          value: example.com/repo/app:latest@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
      - name: sidecar
        image: gcr.io/external/sidecar:v1.2.3
      - name: proxy
        image: docker.io/library/nginx:1.25-alpine
`
		if got != expected {
			t.Errorf("replacePlaceholderImages mismatch (-got +want):\nGOT:\n%s\nWANT:\n%s", got, expected)
		}
	})

	t.Run("untouched external reference (external references are preserved without modification)", func(t *testing.T) {
		externalManifest := `
apiVersion: v1
kind: Pod
metadata:
  name: external-only
spec:
  containers:
  - name: c1
    image: quay.io/coreos/etcd:v3.5.0
  - name: c2
    image: registry.k8s.io/pause:3.10
`
		digests := map[string]string{
			"etcd":  "sha256:1111111111111111111111111111111111111111111111111111111111111111",
			"pause": "sha256:2222222222222222222222222222222222222222222222222222222222222222",
		}
		got, err := replacePlaceholderImages(externalManifest, "example.com/repo", "latest", digests)
		if err != nil {
			t.Fatalf("replacePlaceholderImages failed: %v", err)
		}
		if got != externalManifest {
			t.Errorf("expected external references to remain untouched, got:\n%s", got)
		}
	})

	t.Run("tag@digest with empty tag defaults to latest@sha256:digest", func(t *testing.T) {
		input := "image: app"
		digests := map[string]string{
			"app": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		}
		got, err := replacePlaceholderImages(input, "images.local", "", digests)
		if err != nil {
			t.Fatalf("replacePlaceholderImages failed: %v", err)
		}
		want := "image: images.local/app:latest@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("content hash fallback tag", func(t *testing.T) {
		input := "image: app"
		digests := map[string]string{
			"app": "latest-abcdef123456",
		}
		got, err := replacePlaceholderImages(input, "images.local", "latest", digests)
		if err != nil {
			t.Fatalf("replacePlaceholderImages failed: %v", err)
		}
		want := "image: images.local/app:latest-abcdef123456"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}
