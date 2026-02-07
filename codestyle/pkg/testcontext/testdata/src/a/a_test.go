package a

import (
	"context"
	"testing"
)

func TestSomething(t *testing.T) {
	_ = context.Background() // want "consider using t.Context().*"
	_ = context.TODO()       // want "consider using t.Context().*"
}

func BenchmarkSomething(b *testing.B) {
	_ = context.Background() // want "consider using t.Context().*"
}

func FuzzSomething(f *testing.F) {
	_ = context.TODO() // want "consider using t.Context().*"
}

func helperInTestFile(t testing.TB) {
	_ = context.Background() // want "consider using t.Context().*"
}

func NotATest() {
	_ = context.Background() // want "consider using t.Context().*"
}
