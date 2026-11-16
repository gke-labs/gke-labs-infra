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

// Package collections provides generic data structures.
package collections

import "sync"

// ConcurrentMap is a generic thread-safe map.
type ConcurrentMap[K comparable, V any] interface {
	LookupOrCreate(key K, create func() V) V
}

type concurrentMap[K comparable, V any] struct {
	mutex   sync.Mutex
	entries map[K]V
}

// NewConcurrentMap returns an initialized ConcurrentMap.
func NewConcurrentMap[K comparable, V any]() ConcurrentMap[K, V] {
	return &concurrentMap[K, V]{
		entries: make(map[K]V),
	}
}

// LookupOrCreate returns the value associated with key, or calls create while holding the lock and stores the result.
func (m *concurrentMap[K, V]) LookupOrCreate(key K, create func() V) V {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if val, ok := m.entries[key]; ok {
		return val
	}
	val := create()
	m.entries[key] = val
	return val
}
