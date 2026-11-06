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

package applyset

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"reflect"
	"testing"
)

func TestParentRef_ID(t *testing.T) {
	tests := []struct {
		name    string
		parent  ParentRef
		rawGKNN string
		wantID  string
	}{
		{
			name: "namespaced core ConfigMap",
			parent: ParentRef{
				Name:      "foo",
				Namespace: "default",
				Kind:      "ConfigMap",
				Group:     "",
			},
			rawGKNN: "foo.default.ConfigMap.",
			wantID:  "applyset-PiJv9SLYnBFinWbEN_di177RkEE54FzwT8d03NxfThE-v1",
		},
		{
			name: "cluster-scoped custom resource",
			parent: ParentRef{
				Name:      "cluster-foo",
				Namespace: "",
				Kind:      "ClusterRole",
				Group:     "rbac.authorization.k8s.io",
			},
			rawGKNN: "cluster-foo..ClusterRole.rbac.authorization.k8s.io",
			wantID:  "applyset-yyahHVjARmzs9Dt71RLNWPzVglagaeiM99Wqn2H6Kfo-v1",
		},
		{
			name: "my-app default Secret",
			parent: ParentRef{
				Name:      "my-app",
				Namespace: "default",
				Kind:      "Secret",
				Group:     "",
			},
			rawGKNN: "my-app.default.Secret.",
			wantID:  "applyset-iCeuHqlt3ov6n803ZrPFVTjduUezM6ZFQZ3bDz9dLBo-v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify formula matches kubectl / KEP-3659:
			// applyset-<base64url(sha256("<name>.<namespace>.<kind>.<group>"))>-v1
			h := sha256.Sum256([]byte(tt.rawGKNN))
			expected := fmt.Sprintf("applyset-%s-v1", base64.RawURLEncoding.EncodeToString(h[:]))
			if expected != tt.wantID {
				t.Fatalf("test vector mismatch: expected %q, got %q", tt.wantID, expected)
			}

			got := tt.parent.ID()
			if got != tt.wantID {
				t.Errorf("ParentRef.ID() = %q, want %q", got, tt.wantID)
			}
		})
	}
}

func TestMembership_ParentMetadata(t *testing.T) {
	m := &Membership{}
	m.Add(GroupKind{Group: "apps", Kind: "Deployment"}, "default")
	m.Add(GroupKind{Group: "", Kind: "ConfigMap"}, "default")
	m.Add(GroupKind{Group: "", Kind: "Service"}, "other-ns")
	m.Add(GroupKind{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole"}, "") // cluster-scoped
	m.Add(GroupKind{Group: "apps", Kind: "Deployment"}, "third-ns")               // duplicate GK, new ns

	labels, annotations := m.ParentMetadata("default", "ap/v1.0.0")

	if len(labels) != 0 {
		t.Errorf("expected empty labels, got: %v", labels)
	}

	if got := annotations[ToolingAnnotation]; got != "ap/v1.0.0" {
		t.Errorf("tooling annotation = %q, want %q", got, "ap/v1.0.0")
	}

	// ContainsGroupKinds: ConfigMap, Deployment.apps, Service, ClusterRole.rbac.authorization.k8s.io
	// Alphabetically sorted:
	// ClusterRole.rbac.authorization.k8s.io, ConfigMap, Deployment.apps, Service
	wantCGK := "ClusterRole.rbac.authorization.k8s.io,ConfigMap,Deployment.apps,Service"
	if got := annotations[ContainsGroupKindsAnnotation]; got != wantCGK {
		t.Errorf("contains-group-kinds = %q, want %q", got, wantCGK)
	}

	// AdditionalNamespaces: namespaces other than parent ("default") and not empty ("")
	// "other-ns", "third-ns"
	wantAddNS := "other-ns,third-ns"
	if got := annotations[AdditionalNamespacesAnnotation]; got != wantAddNS {
		t.Errorf("additional-namespaces = %q, want %q", got, wantAddNS)
	}
}

func TestMembership_ParentMetadata_NoAdditionalNamespaces(t *testing.T) {
	m := &Membership{}
	m.Add(GroupKind{Group: "", Kind: "ConfigMap"}, "default")
	m.Add(GroupKind{Group: "apps", Kind: "Deployment"}, "default")

	_, annotations := m.ParentMetadata("default", "ap/v1.0.0")

	if _, ok := annotations[AdditionalNamespacesAnnotation]; ok {
		t.Errorf("additional-namespaces annotation should be omitted when empty, got: %v", annotations[AdditionalNamespacesAnnotation])
	}
}

func TestParseParentRef(t *testing.T) {
	tests := []struct {
		input   string
		want    ParentRef
		wantErr bool
	}{
		{
			input: "foo",
			want: ParentRef{
				Kind:     "Secret",
				Group:    "",
				Name:     "foo",
				Resource: "secrets",
			},
		},
		{
			input: "configmaps/foo",
			want: ParentRef{
				Kind:     "ConfigMap",
				Group:    "",
				Name:     "foo",
				Resource: "configmaps",
			},
		},
		{
			input: "secrets/bar",
			want: ParentRef{
				Kind:     "Secret",
				Group:    "",
				Name:     "bar",
				Resource: "secrets",
			},
		},
		{
			input: "packages.infra.labs.gke.io/my-pkg",
			want: ParentRef{
				Kind:     "packages",
				Group:    "infra.labs.gke.io",
				Name:     "my-pkg",
				Resource: "packages",
			},
		},
		{
			input:   "configmaps/custom-ns/foo", // 3 parts is rejected: only [RESOURCE[.GROUP]/]NAME
			wantErr: true,
		},
		{
			input:   "",
			wantErr: true,
		},
		{
			input:   "a/b/c/d",
			wantErr: true,
		},
		{
			input:   "/foo",
			wantErr: true,
		},
		{
			input:   "configmaps/",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseParentRef(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseParentRef(%q) err = %v, wantErr = %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseParentRef(%q) = %+v, want %+v", tt.input, got, tt.want)
			}
		})
	}
}
