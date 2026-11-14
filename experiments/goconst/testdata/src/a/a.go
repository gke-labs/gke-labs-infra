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

type Meta struct {
	Name string
}

func (m *Meta) SetName(name string) {
	m.Name = name
}

func (m Meta) GetName() string {
	return m.Name
}

type Foo struct {
	Meta
	Bar   int
	Data  map[string]string
	Items []int
}

func (f *Foo) Mutate() {
	f.Bar = 99
}

func (f Foo) ValueMethod() int {
	return f.Bar
}

func main() {
	f := &Foo{Bar: 1, Data: make(map[string]string), Items: []int{1, 2, 3}}
	c := WrapConst(f)

	var p *Foo
	p = c // want "implicit conversion from Const\\[T\\] to \\*T"

	m := make(map[string]*Foo)
	m["foo"] = c // want "implicit conversion from Const\\[T\\] to \\*T"

	takePtr(c) // want "implicit conversion from Const\\[T\\] to \\*T"

	takeConst(c) // OK

	// Explicit conversion
	p = (*Foo)(c) // want "explicit conversion from Const\\[T\\] to \\*T"
	_ = p

	// Struct field
	type Container struct {
		Ptr *Foo
	}
	_ = Container{
		Ptr: c, // want "implicit conversion from Const\\[T\\] to \\*T"
	}

	// 1. Field assignment
	c.Bar = 5 // want "cannot assign to field through Const\\[T\\]"
	c.Bar++   // want "cannot modify value through Const\\[T\\]"

	// 2. Map and slice element writes
	c.Data["k"] = "w"   // want "cannot assign to map element through Const\\[T\\]"
	c.Items[0] = 10     // want "cannot assign to slice element through Const\\[T\\]"
	delete(c.Data, "k") // want "cannot delete from map through Const\\[T\\]"

	// 3. Append to slice
	_ = append(c.Items, 4) // want "cannot append to slice through Const\\[T\\]"

	// 4. Taking address
	_ = &c.Bar  // want "cannot take address through Const\\[T\\]"
	_ = &(*c)   // want "cannot take address through Const\\[T\\]"
	_ = &c.Meta // want "cannot take address through Const\\[T\\]"

	// 5. Method calls with pointer receiver
	c.Meta.SetName("new") // want "cannot call pointer-receiver method on Const\\[T\\]"
	(*c).Mutate()         // want "cannot call pointer-receiver method on Const\\[T\\]"
	_ = (*c).ValueMethod() // OK (value receiver)
	_ = c.Meta.GetName()  // OK (value receiver on embedded field)

	// 6. Aliasing through local variable
	localMap := c.Data
	localMap["foo"] = "bar" // want "cannot assign to map element through Const\\[T\\]"

	localSlice := c.Items
	localSlice[1] = 20 // want "cannot assign to slice element through Const\\[T\\]"
}

func takePtr(f *Foo)         {}
func takeConst(f Const[Foo]) {}

func returnPtr(c Const[Foo]) *Foo {
	return c // want "conversion from Const\\[T\\] to \\*T in return statement"
}
