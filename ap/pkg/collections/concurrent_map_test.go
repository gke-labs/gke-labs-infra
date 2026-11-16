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

package collections

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestConcurrentMap(t *testing.T) {
	m := NewConcurrentMap[int, *int]()

	const keys = 3
	const goroutines = 60

	var creates [keys]atomic.Int32
	results := make([]*int, goroutines)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			key := idx % keys
			results[idx] = m.LookupOrCreate(key, func() *int {
				creates[key].Add(1)
				val := key
				return &val
			})
		}(i)
	}
	wg.Wait()

	for k := 0; k < keys; k++ {
		if count := creates[k].Load(); count != 1 {
			t.Errorf("key %d created %d times, want 1", k, count)
		}
	}
	for i := 0; i < goroutines; i++ {
		if results[i] != results[i%keys] {
			t.Errorf("results[%d] != results[%d]: got %p, want %p", i, i%keys, results[i], results[i%keys])
		}
	}
}
