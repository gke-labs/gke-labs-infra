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

package commands

import (
	"reflect"
	"testing"
)

func TestSplitYAML(t *testing.T) {
	tests := []struct {
		name string
		data string
		want []string
	}{
		{
			name: "Single doc",
			data: "foo: bar\n",
			want: []string{"foo: bar\n"},
		},
		{
			name: "Multi doc",
			data: "foo: bar\n---\nbaz: qux\n",
			want: []string{"foo: bar", "baz: qux\n"},
		},
		{
			name: "Multi doc with surrounding newlines",
			data: "foo: bar\n\n---\n\nbaz: qux\n",
			want: []string{"foo: bar\n", "\nbaz: qux\n"},
		},
		{
			name: "Start with separator",
			data: "---\nfoo: bar\n",
			want: []string{"foo: bar\n"},
		},
		{
			name: "End with separator",
			data: "foo: bar\n---\n",
			want: []string{"foo: bar"},
		},
		{
			name: "Multiple separators",
			data: "doc1\n---\ndoc2\n---\ndoc3",
			want: []string{"doc1", "doc2", "doc3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBytes := SplitYAML([]byte(tt.data))
			var got []string
			for _, b := range gotBytes {
				got = append(got, string(b))
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("SplitYAML() = %q, want %q", got, tt.want)
			}
		})
	}
}
