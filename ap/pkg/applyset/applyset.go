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

// Package applyset implements the ApplySet specification for tracking object
// sets in Kubernetes (KEP-3659).
//
// Note: We deliberately follow the kubectl implementation rather than the KEP-3659
// text where they diverge: the KEP text uses "applyset.k8s.io/...", lists "tooling"
// as a label, names the CRD label "applyset.k8s.io/role/parent", and gives the ID
// as a bare base64 hash. In contrast, kubectl (which other tools interoperate
// with) ships "applyset.kubernetes.io/...", "tooling" as an annotation,
// "applyset.kubernetes.io/is-parent-type", and the "applyset-<b64>-v1" formatted
// ID. We follow kubectl so our ApplySets interoperate directly with kubectl and
// the broader ecosystem.
package applyset

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ApplySet label and annotation keys according to KEP-3659.
const (
	PartOfLabel                    = "applyset.kubernetes.io/part-of"
	IDLabel                        = "applyset.kubernetes.io/id"
	ToolingAnnotation              = "applyset.kubernetes.io/tooling"
	ContainsGroupKindsAnnotation   = "applyset.kubernetes.io/contains-group-kinds"
	AdditionalNamespacesAnnotation = "applyset.kubernetes.io/additional-namespaces"
	IsParentTypeLabel              = "applyset.kubernetes.io/is-parent-type"
)

// GroupKind represents a Kubernetes Group and Kind.
type GroupKind struct {
	Group string
	Kind  string
}

// ParentRef represents an ApplySet parent object reference.
// The spec hashes the Kind, so resource names from flags or references
// are resolved to Kind by the caller.
type ParentRef struct {
	Group     string
	Kind      string
	Namespace string
	Name      string
	Resource  string
}

// ID computes the unique ApplySet identifier for the parent object:
// applyset-<base64url(sha256("<name>.<namespace>.<kind>.<group>"))>-v1
// Namespace is empty for cluster-scoped parents.
func (p ParentRef) ID() string {
	data := fmt.Sprintf("%s.%s.%s.%s", p.Name, p.Namespace, p.Kind, p.Group)
	h := sha256.Sum256([]byte(data))
	b64 := base64.RawURLEncoding.EncodeToString(h[:])
	return fmt.Sprintf("applyset-%s-v1", b64)
}

// Membership collects GroupKinds and namespaces of member objects applied in an ApplySet.
type Membership struct {
	mu         sync.Mutex
	GroupKinds []GroupKind
	Namespaces []string
}

// Add records a GroupKind and namespace in the membership.
func (m *Membership) Add(gk GroupKind, ns string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.GroupKinds = append(m.GroupKinds, gk)
	if ns != "" {
		m.Namespaces = append(m.Namespaces, ns)
	}
}

// ParentMetadata produces the labels and annotations for the parent object.
// ContainsGroupKinds is sorted and comma-separated with format "Kind.group" (or "Kind" for core group).
// AdditionalNamespaces contains sorted namespaces other than parentNamespace.
func (m *Membership) ParentMetadata(parentNamespace, tooling string) (labels, annotations map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	labels = make(map[string]string)
	annotations = make(map[string]string)

	if tooling != "" {
		annotations[ToolingAnnotation] = tooling
	}

	// Format contains-group-kinds
	gkSet := make(map[string]bool)
	var gkList []string
	for _, gk := range m.GroupKinds {
		str := gk.Kind
		if gk.Group != "" {
			str = fmt.Sprintf("%s.%s", gk.Kind, gk.Group)
		}
		if str != "" && !gkSet[str] {
			gkSet[str] = true
			gkList = append(gkList, str)
		}
	}
	sort.Strings(gkList)
	if len(gkList) > 0 {
		annotations[ContainsGroupKindsAnnotation] = strings.Join(gkList, ",")
	}

	// Format additional-namespaces
	nsSet := make(map[string]bool)
	var nsList []string
	for _, ns := range m.Namespaces {
		if ns != "" && ns != parentNamespace && !nsSet[ns] {
			nsSet[ns] = true
			nsList = append(nsList, ns)
		}
	}
	sort.Strings(nsList)
	if len(nsList) > 0 {
		annotations[AdditionalNamespacesAnnotation] = strings.Join(nsList, ",")
	}

	return labels, annotations
}

// ParseParentRef parses an ApplySet parent reference string into a ParentRef.
// Supported formats match kubectl: [RESOURCE[.GROUP]/]NAME
func ParseParentRef(s string) (ParentRef, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return ParentRef{}, fmt.Errorf("applyset parent reference cannot be empty")
	}

	parts := strings.Split(s, "/")
	var resGroup, name string

	switch len(parts) {
	case 1:
		name = parts[0]
		resGroup = "secrets"
	case 2:
		resGroup = parts[0]
		name = parts[1]
	default:
		return ParentRef{}, fmt.Errorf("invalid applyset parent reference %q; expected [RESOURCE[.GROUP]/]NAME", s)
	}

	if name == "" {
		return ParentRef{}, fmt.Errorf("invalid applyset parent reference %q: name cannot be empty", s)
	}
	if resGroup == "" {
		return ParentRef{}, fmt.Errorf("invalid applyset parent reference %q: resource cannot be empty", s)
	}

	var resource, group string
	if r, g, ok := strings.Cut(resGroup, "."); ok {
		resource = r
		group = g
	} else {
		resource = resGroup
	}

	if resource == "" {
		return ParentRef{}, fmt.Errorf("invalid applyset parent reference %q: resource cannot be empty", s)
	}

	kind := resource
	switch strings.ToLower(resource) {
	case "configmap", "configmaps":
		kind = "ConfigMap"
		group = ""
	case "secret", "secrets":
		kind = "Secret"
		group = ""
	}

	return ParentRef{
		Group:    group,
		Kind:     kind,
		Name:     name,
		Resource: resource,
	}, nil
}
