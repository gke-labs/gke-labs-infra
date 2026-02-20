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

package a

type Const[T any] *T

func WrapConst[T any](v *T) Const[T] { return Const[T](v) }

type Foo struct {
	Bar int
}

func main() {
	f := &Foo{Bar: 1}
	c := WrapConst(f)

	var p *Foo
	p = c // want "implicit conversion"

	m := make(map[string]*Foo)
	m["foo"] = c // want "implicit conversion"

	takePtr(c) // want "implicit conversion"

	takeConst(c) // OK

	// Explicit conversion
	p = (*Foo)(c) // OK
	_ = p

	// Struct field
	type Container struct {
		Ptr *Foo
	}
	_ = Container{
		Ptr: c, // want "implicit conversion"
	}
}

func takePtr(f *Foo)         {}
func takeConst(f Const[Foo]) {}
