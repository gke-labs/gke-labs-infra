package a

import (
	"context"
	"testing"
)

func Helper(t *testing.T) {
	_ = context.Background() // want "consider using t.Context().*"
}

func NormalFunc() {
	_ = context.Background() // OK
}

func AnotherHelper(t testing.TB) {
	_ = context.TODO() // want "consider using t.Context().*"
}
