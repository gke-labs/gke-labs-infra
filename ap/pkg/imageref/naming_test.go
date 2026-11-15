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
	"fmt"
	"sync"
	"testing"
)

func TestImageNaming_ConcurrentRef(t *testing.T) {
	naming := NewImageNaming("images.local", "latest", false)

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	refs := make([]*ImageRef, goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			// Alternate between shared image names and unique image names.
			imgName := fmt.Sprintf("img-%d", idx%5)
			refs[idx] = naming.Ref(imgName)
		}(i)
	}
	wg.Wait()

	// Verify that calls with the same name returned the exact same pointer.
	for i := 0; i < goroutines; i++ {
		expectedName := fmt.Sprintf("img-%d", i%5)
		if refs[i].Name != expectedName {
			t.Errorf("refs[%d].Name = %q, want %q", i, refs[i].Name, expectedName)
		}
		if refs[i] != refs[i%5] {
			t.Errorf("refs[%d] != refs[%d]: expected identical pointer for image %q", i, i%5, expectedName)
		}
	}
}

func TestImageNaming_UninitializedRef(t *testing.T) {
	// ImageNaming without NewImageNaming or WithRefs (refs and mu are nil)
	naming := ImageNaming{
		Prefix: "my-repo",
		Tag:    "v1",
	}

	ref1 := naming.Ref("app")
	if ref1.String() != "my-repo/app:v1" {
		t.Errorf("ref1.String() = %q, want %q", ref1.String(), "my-repo/app:v1")
	}

	ref2 := naming.Ref("app")
	if ref2.String() != "my-repo/app:v1" {
		t.Errorf("ref2.String() = %q, want %q", ref2.String(), "my-repo/app:v1")
	}

	// Concurrent calls on uninitialized naming should also be safe
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = naming.Ref(fmt.Sprintf("img-%d", idx))
		}(i)
	}
	wg.Wait()
}

func TestImageNaming_WithRefs(t *testing.T) {
	naming := ImageNaming{
		Prefix: "my-repo",
		Tag:    "v2",
	}
	naming = naming.WithRefs()

	ref1 := naming.Ref("app")
	ref2 := naming.Ref("app")
	if ref1 != ref2 {
		t.Errorf("expected ref1 and ref2 to be identical pointers after WithRefs()")
	}
}
