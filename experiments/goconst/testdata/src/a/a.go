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

func takePtr(f *Foo) {}
func takeConst(f Const[Foo]) {}
