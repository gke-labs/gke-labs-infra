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

package goconst

import (
	"runtime"
	"testing"
)

type Foo struct {
	Bar int
	Baz string
}

func TestConst(t *testing.T) {
	f := &Foo{Bar: 1, Baz: "hello"}

	// Mark as constant
	Const(f)

	// Trigger check, should be no error
	if err := Check(); err != nil {
		t.Errorf("Check() failed: %v", err)
	}

	// Mutate
	f.Bar = 2

	// Trigger check, should detect change
	if err := Check(); err == nil {
		t.Error("Check() should have detected mutation, but it didn't")
	} else {
		t.Logf("Detected expected mutation: %v", err)
	}
}

func TestWeakReference(t *testing.T) {
	// Clear any previous state
	trackedMutex.Lock()
	tracked = nil
	trackedMutex.Unlock()

	{
		f := &Foo{Bar: 1}
		Const(f)
		trackedMutex.Lock()
		if len(tracked) != 1 {
			t.Errorf("Expected 1 tracked object, got %d", len(tracked))
		}
		trackedMutex.Unlock()

		// f is still in scope here
		if err := Check(); err != nil {
			t.Errorf("Check() failed: %v", err)
		}
	}

	// f is now out of scope and can be collected.
	// We might need to run GC multiple times or wait a bit for weak pointers to be cleared.
	runtime.GC()
	runtime.GC()

	// Check should clean up the tracked list
	_ = Check()

	trackedMutex.Lock()
	defer trackedMutex.Unlock()
	if len(tracked) != 0 {
		t.Errorf("Expected 0 tracked objects after GC, got %d", len(tracked))
	}
}
