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

package registrypush

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

const (
	// DefaultNamespace is the Kubernetes namespace where the in-cluster registry runs.
	DefaultNamespace = "in-cluster-image-registry-system"
	// DefaultService is the service name of the in-cluster image registry.
	DefaultService = "in-cluster-image-registry"
	// DefaultPort is the container port of the in-cluster image registry.
	DefaultPort = 5000
	// DefaultRegistry is the default registry hostname used by in-cluster containerd.
	DefaultRegistry = "images.local"
)

// Push pushes one image from an OCI layout directory on disk using the provided transport.
func Push(ctx context.Context, transport http.RoundTripper, directory, repository, tag string) error {
	referenceString := fmt.Sprintf("%s:%s", repository, tag)
	imageTag, err := name.NewTag(referenceString, name.Insecure, name.WithDefaultRegistry(DefaultRegistry))
	if err != nil {
		return fmt.Errorf("parsing image reference %s: %w", referenceString, err)
	}

	layoutPath, err := layout.FromPath(directory)
	if err != nil {
		return fmt.Errorf("opening OCI layout directory %s: %w", directory, err)
	}

	index, err := layoutPath.ImageIndex()
	if err != nil {
		return fmt.Errorf("reading image index from %s: %w", directory, err)
	}

	indexManifest, err := index.IndexManifest()
	if err != nil {
		return fmt.Errorf("reading index manifest from %s: %w", directory, err)
	}

	if len(indexManifest.Manifests) == 0 {
		return fmt.Errorf("no manifests found in index.json at %s", directory)
	}
	if len(indexManifest.Manifests) > 1 {
		var manifestDigests []string
		for _, manifestDescriptor := range indexManifest.Manifests {
			manifestDigests = append(manifestDigests, manifestDescriptor.Digest.String())
		}
		return fmt.Errorf("index.json at %s references multiple manifests (%s); expected exactly one", directory, strings.Join(manifestDigests, ", "))
	}

	targetDescriptor := indexManifest.Manifests[0]
	remoteOptions := []remote.Option{
		remote.WithTransport(transport),
		remote.WithContext(ctx),
	}

	if targetDescriptor.MediaType.IsIndex() {
		childIndex, err := index.ImageIndex(targetDescriptor.Digest)
		if err != nil {
			return fmt.Errorf("reading image index %s: %w", targetDescriptor.Digest, err)
		}
		return remote.WriteIndex(imageTag, childIndex, remoteOptions...)
	}

	image, err := layoutPath.Image(targetDescriptor.Digest)
	if err != nil {
		return fmt.Errorf("reading image %s: %w", targetDescriptor.Digest, err)
	}
	return remote.Write(imageTag, image, remoteOptions...)
}
