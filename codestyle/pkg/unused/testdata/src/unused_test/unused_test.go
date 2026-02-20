// Copyright 2026 Google LLC
package unused_test

type Const[T any] struct {
	val T
}

func (c *Const[T]) Read() T {
	return c.val
}

type UnusedStruct struct {
	field int // want "field field is unused"
}

func unusedFunc() { // want "func unusedFunc is unused"
}

func usedFunc() {
}

func Main() {
	usedFunc()
	c := &Const[int]{val: 1}
	_ = c.Read()
}
