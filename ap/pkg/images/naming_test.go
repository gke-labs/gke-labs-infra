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

package images

import (
	"testing"
)

func TestResolveImageNaming(t *testing.T) {
	tests := []struct {
		name           string
		envPrefix      string
		envTag         string
		inCluster      bool
		imageName      string
		buildkitHost   string
		wantPrefix     string
		wantTag        string
		wantRef        string
		wantPushPrefix string
		wantPushRef    string
	}{
		{
			name:           "unset env vars defaults to images.local and latest",
			envPrefix:      "",
			envTag:         "",
			inCluster:      false,
			imageName:      "test-app",
			buildkitHost:   "",
			wantPrefix:     "images.local",
			wantTag:        "latest",
			wantRef:        "images.local/test-app:latest",
			wantPushPrefix: "images.local",
			wantPushRef:    "images.local/test-app:latest",
		},
		{
			name:           "set env vars uses specified prefix and tag",
			envPrefix:      "example.com/team",
			envTag:         "v1.2.3",
			inCluster:      false,
			imageName:      "test-app",
			buildkitHost:   "",
			wantPrefix:     "example.com/team",
			wantTag:        "v1.2.3",
			wantRef:        "example.com/team/test-app:v1.2.3",
			wantPushPrefix: "example.com/team",
			wantPushRef:    "example.com/team/test-app:v1.2.3",
		},
		{
			name:           "in-cluster rewrite with InCluster true",
			envPrefix:      "",
			envTag:         "v2.0",
			inCluster:      true,
			imageName:      "controller",
			buildkitHost:   "",
			wantPrefix:     "images.local",
			wantTag:        "v2.0",
			wantRef:        "images.local/controller:v2.0",
			wantPushPrefix: "in-cluster-image-registry.in-cluster-image-registry-system.svc.cluster.local:80",
			wantPushRef:    "in-cluster-image-registry.in-cluster-image-registry-system.svc.cluster.local:80/controller:v2.0",
		},
		{
			name:           "in-cluster rewrite with k8s buildkit host",
			envPrefix:      "",
			envTag:         "v2.0",
			inCluster:      false,
			imageName:      "controller",
			buildkitHost:   "k8s://autodeploy-system/buildkit",
			wantPrefix:     "images.local",
			wantTag:        "v2.0",
			wantRef:        "images.local/controller:v2.0",
			wantPushPrefix: "in-cluster-image-registry.in-cluster-image-registry-system.svc.cluster.local:80",
			wantPushRef:    "in-cluster-image-registry.in-cluster-image-registry-system.svc.cluster.local:80/controller:v2.0",
		},
		{
			name:           "custom registry with k8s buildkit host preserves prefix",
			envPrefix:      "gcr.io/my-project",
			envTag:         "latest",
			inCluster:      true,
			imageName:      "worker",
			buildkitHost:   "k8s://autodeploy-system/buildkit",
			wantPrefix:     "gcr.io/my-project",
			wantTag:        "latest",
			wantRef:        "gcr.io/my-project/worker:latest",
			wantPushPrefix: "gcr.io/my-project",
			wantPushRef:    "gcr.io/my-project/worker:latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("IMAGE_PREFIX", tt.envPrefix)
			t.Setenv("IMAGE_TAG", tt.envTag)

			naming := ResolveImageNaming(nil)
			naming.InCluster = tt.inCluster

			if naming.Prefix != tt.wantPrefix {
				t.Errorf("naming.Prefix = %q, want %q", naming.Prefix, tt.wantPrefix)
			}
			if naming.Tag != tt.wantTag {
				t.Errorf("naming.Tag = %q, want %q", naming.Tag, tt.wantTag)
			}

			ref := naming.Ref(tt.imageName)
			if got := ref.String(); got != tt.wantRef {
				t.Errorf("naming.Ref(%q).String() = %q, want %q", tt.imageName, got, tt.wantRef)
			}

			pushPrefix := naming.PushPrefix(tt.buildkitHost)
			if pushPrefix != tt.wantPushPrefix {
				t.Errorf("naming.PushPrefix(%q) = %q, want %q", tt.buildkitHost, pushPrefix, tt.wantPushPrefix)
			}

			pushRef := ImageRef{Prefix: pushPrefix, Name: tt.imageName, Tag: naming.Tag}.String()
			if pushRef != tt.wantPushRef {
				t.Errorf("pushRef = %q, want %q", pushRef, tt.wantPushRef)
			}
		})
	}
}

func TestImageRef_String_Digest(t *testing.T) {
	tests := []struct {
		name string
		ref  ImageRef
		want string
	}{
		{
			name: "without digest",
			ref:  ImageRef{Prefix: "my-repo", Name: "app", Tag: "v1"},
			want: "my-repo/app:v1",
		},
		{
			name: "with sha256 prefix digest",
			ref:  ImageRef{Prefix: "my-repo", Name: "app", Tag: "latest", Digest: "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"},
			want: "my-repo/app:latest@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		},
		{
			name: "with raw hex digest",
			ref:  ImageRef{Prefix: "my-repo", Name: "app", Tag: "latest", Digest: "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"},
			want: "my-repo/app:latest@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		},
		{
			name: "with unique tag (kind fallback)",
			ref:  ImageRef{Prefix: "images.local", Name: "app", Tag: "latest-abcdef123456"},
			want: "images.local/app:latest-abcdef123456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ref.String(); got != tt.want {
				t.Errorf("ref.String() = %q, want %q", got, tt.want)
			}
		})
	}
}
