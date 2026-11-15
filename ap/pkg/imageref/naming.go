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

package imageref

import (
	"strings"
	"sync"
)

// ImageRef represents a reference to an image with prefix, name, tag, and optional digest.
// Tag is never empty ("latest" when unset).
type ImageRef struct {
	Prefix string
	Name   string
	Tag    string
	Digest string
}

// String returns the image reference formatted as prefix/name:tag, or name:tag if prefix is empty.
// When Digest is present, it returns prefix/name:tag@sha256:<digest>.
func (r ImageRef) String() string {
	tag := r.Tag
	if tag == "" {
		tag = "latest"
	}
	var nameTag string
	if r.Prefix != "" {
		nameTag = r.Prefix + "/" + r.Name + ":" + tag
	} else {
		nameTag = r.Name + ":" + tag
	}
	if r.Digest != "" {
		digest := r.Digest
		if !strings.HasPrefix(digest, "sha256:") {
			digest = "sha256:" + digest
		}
		return nameTag + "@" + digest
	}
	return nameTag
}

// ParseImageRef parses an image string into an *ImageRef.
func ParseImageRef(s string) (*ImageRef, error) {
	r, err := Parse(s)
	if err != nil {
		return nil, err
	}
	return &ImageRef{
		Prefix: r.Registry,
		Name:   r.Repository,
		Tag:    r.Tag,
		Digest: r.Digest,
	}, nil
}

// ImageNaming holds the naming conventions for images in a build or deploy run.
type ImageNaming struct {
	Prefix    string
	Tag       string
	InCluster bool
	KindMode  bool
	mu        *sync.Mutex
	refs      map[string]*ImageRef
}

// NewImageNaming creates an ImageNaming value with an initialized references map.
func NewImageNaming(prefix, tag string, inCluster bool) ImageNaming {
	if prefix == "" {
		prefix = "images.local"
	}
	if tag == "" {
		tag = "latest"
	}
	return ImageNaming{
		Prefix:    prefix,
		Tag:       tag,
		InCluster: inCluster,
		mu:        &sync.Mutex{},
		refs:      make(map[string]*ImageRef),
	}
}

// WithRefs returns a copy of ImageNaming with an initialized references map.
func (n ImageNaming) WithRefs() ImageNaming {
	if n.refs == nil {
		n.refs = make(map[string]*ImageRef)
	}
	if n.mu == nil {
		n.mu = &sync.Mutex{}
	}
	return n
}

// Ref returns an ImageRef for the given image name.
func (n ImageNaming) Ref(name string) *ImageRef {
	if n.mu != nil {
		n.mu.Lock()
		defer n.mu.Unlock()
	}
	if n.refs != nil {
		if ref, ok := n.refs[name]; ok {
			return ref
		}
	}
	tag := n.Tag
	if tag == "" {
		tag = "latest"
	}
	ref := &ImageRef{
		Prefix: n.Prefix,
		Name:   name,
		Tag:    tag,
	}
	if n.refs != nil {
		n.refs[name] = ref
	}
	return ref
}

// PushPrefix returns the registry prefix to use when pushing images.
// For the default "images.local" prefix, it rewrites to the in-cluster registry
// (if running in-cluster or using a k8s buildkit host) or localhost:5000.
// For any other prefix, it returns Prefix unchanged.
func (n ImageNaming) PushPrefix(buildkitHost string) string {
	if n.Prefix == "images.local" {
		if n.InCluster || strings.HasPrefix(buildkitHost, "k8s://") {
			return "in-cluster-image-registry.in-cluster-image-registry-system.svc.cluster.local:80"
		}
		return "localhost:5000"
	}
	return n.Prefix
}
