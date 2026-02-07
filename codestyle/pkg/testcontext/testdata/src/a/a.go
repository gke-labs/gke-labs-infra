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
