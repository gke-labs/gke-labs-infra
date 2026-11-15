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
