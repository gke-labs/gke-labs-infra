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
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
	"weak"
)

var (
	// trackedMutex guards tracked.
	trackedMutex sync.Mutex
	// tracked is a list of all objects marked const that we are tracking.
	tracked []func() (bool, error)
)

// MarkConst marks a value as constant. It returns the value itself for convenience.
// It tracks the object pointed to by val.
func MarkConst[T any](val *T) *T {
	hash := computeHash(val)
	ptr := weak.Make(val)

	trackedMutex.Lock()
	defer trackedMutex.Unlock()

	tracked = append(tracked, func() (bool, error) {
		v := ptr.Value()
		if v == nil {
			return false, nil
		}
		newHash := computeHash(v)
		if newHash != hash {
			return true, fmt.Errorf("detected mutation in %T: was %s, now %s", v, hash, newHash)
		}
		return true, nil
	})
	return val
}

// Const is a type-safe wrapper for a constant value.
type Const[T any] struct {
	val *T
}

// Read returns the constant value.
func (c Const[T]) Read() *T {
	return c.val
}

// WrapConst wraps a value and marks it as constant.
func WrapConst[T any](val *T) Const[T] {
	MarkConst(val)
	return Const[T]{val: val}
}

func computeHash(val any) string {
	b, err := json.Marshal(val)
	if err != nil {
		panic(fmt.Sprintf("error-marshaling-%T: %v", val, err))
	}
	return string(b)
}

// Check triggers a change detection poll for all tracked constant objects.
// It returns an error if any mutation is detected.
func Check() error {
	trackedMutex.Lock()
	defer trackedMutex.Unlock()

	var stillTracked []func() (bool, error)
	var errs []error

	for _, check := range tracked {
		alive, err := check()
		if err != nil {
			errs = append(errs, err)
		}
		if alive {
			stillTracked = append(stillTracked, check)
		}
	}
	tracked = stillTracked

	return errors.Join(errs...)
}

func init() {
	go func() {
		for {
			time.Sleep(time.Minute)
			if err := Check(); err != nil {
				panic(err)
			}
		}
	}()
}
