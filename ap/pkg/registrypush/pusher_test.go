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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

type trackedRegistry struct {
	handler       http.Handler
	blobUploads   atomic.Int32
	uploadedBlobs sync.Map
}

func (tracked *trackedRegistry) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodPut && strings.Contains(request.URL.Path, "/blobs/uploads/") {
		digest := request.URL.Query().Get("digest")
		if digest != "" {
			tracked.blobUploads.Add(1)
			tracked.uploadedBlobs.Store(digest, true)
		}
	}
	tracked.handler.ServeHTTP(writer, request)
}

func setupTestRegistry(t *testing.T) (*trackedRegistry, string, http.RoundTripper) {
	t.Helper()
	tracked := &trackedRegistry{
		handler: registry.New(),
	}
	server := httptest.NewServer(tracked)
	t.Cleanup(server.Close)
	return tracked, server.Listener.Addr().String(), server.Client().Transport
}

func createTestOCILayout(t *testing.T, directory string, layerData ...string) {
	t.Helper()
	image := empty.Image
	for _, data := range layerData {
		layer := static.NewLayer([]byte(data), types.OCILayer)
		var err error
		image, err = mutate.AppendLayers(image, layer)
		if err != nil {
			t.Fatalf("mutate.AppendLayers failed: %v", err)
		}
	}

	layoutPath, err := layout.Write(directory, empty.Index)
	if err != nil {
		t.Fatalf("layout.Write failed: %v", err)
	}
	if err := layoutPath.AppendImage(image); err != nil {
		t.Fatalf("layoutPath.AppendImage failed: %v", err)
	}
}

func TestPush_LifecycleAndDeduplication(t *testing.T) {
	tracker, host, transport := setupTestRegistry(t)

	directory := t.TempDir()
	createTestOCILayout(t, directory, "base layer content", "top layer v1 content")

	// 1. First push uploads all blobs (base layer, top layer v1, config blob)
	err := Push(t.Context(), transport, directory, host+"/testrepo", "v1")
	if err != nil {
		t.Fatalf("first Push failed: %v", err)
	}

	firstUploads := tracker.blobUploads.Load()
	if firstUploads != 3 {
		t.Errorf("expected 3 blob uploads on first push, got %d", firstUploads)
	}

	// 2. Second push of the same image uploads no blobs
	tracker.blobUploads.Store(0)
	err = Push(t.Context(), transport, directory, host+"/testrepo", "v1")
	if err != nil {
		t.Fatalf("second Push failed: %v", err)
	}

	secondUploads := tracker.blobUploads.Load()
	if secondUploads != 0 {
		t.Errorf("expected 0 blob uploads on second push, got %d", secondUploads)
	}

	// 3. Changed top layer uploads only the new blobs (new top layer + new config)
	directory2 := t.TempDir()
	createTestOCILayout(t, directory2, "base layer content", "top layer v2 content")

	tracker.blobUploads.Store(0)
	err = Push(t.Context(), transport, directory2, host+"/testrepo", "v2")
	if err != nil {
		t.Fatalf("Push with changed top layer failed: %v", err)
	}

	thirdUploads := tracker.blobUploads.Load()
	if thirdUploads != 2 {
		t.Errorf("expected 2 blob uploads (new top layer + new config), got %d", thirdUploads)
	}

	// 4. Directory with stale unreferenced blobs pushes only what index.json references
	staleBlobPath := filepath.Join(directory2, "blobs", "sha256", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := os.WriteFile(staleBlobPath, []byte("stale unreferenced blob"), 0644); err != nil {
		t.Fatalf("failed to write stale blob: %v", err)
	}

	tracker.blobUploads.Store(0)
	err = Push(t.Context(), transport, directory2, host+"/testrepo", "v2")
	if err != nil {
		t.Fatalf("Push with stale blobs failed: %v", err)
	}

	staleHash := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, found := tracker.uploadedBlobs.Load(staleHash); found {
		t.Errorf("stale blob was uploaded, but should have been ignored")
	}
}

func TestPush_MultipleManifestsError(t *testing.T) {
	_, host, transport := setupTestRegistry(t)

	directory := t.TempDir()
	createTestOCILayout(t, directory, "layer a")

	layoutPath, err := layout.FromPath(directory)
	if err != nil {
		t.Fatalf("layout.FromPath failed: %v", err)
	}

	secondImage, err := mutate.AppendLayers(empty.Image, static.NewLayer([]byte("layer b"), types.OCILayer))
	if err != nil {
		t.Fatalf("mutate.AppendLayers failed: %v", err)
	}
	if err := layoutPath.AppendImage(secondImage); err != nil {
		t.Fatalf("layoutPath.AppendImage failed: %v", err)
	}

	err = Push(t.Context(), transport, directory, host+"/testrepo", "v1")
	if err == nil {
		t.Fatal("expected error when index.json references multiple manifests, got nil")
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "references multiple manifests") {
		t.Errorf("error should describe multiple manifests, got: %s", errStr)
	}
	if !strings.Contains(errStr, "expected exactly one") {
		t.Errorf("error should state expected exactly one, got: %s", errStr)
	}
}
