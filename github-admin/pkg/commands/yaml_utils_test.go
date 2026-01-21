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
			want: []string{"foo: bar\n", "\nbaz: qux\n"},
		},
		{
			name: "Multi doc with surrounding newlines",
			data: "foo: bar\n\n---\n\nbaz: qux\n",
			want: []string{"foo: bar\n\n", "\n\nbaz: qux\n"},
		},
		{
			name: "Start with separator",
			data: "---\nfoo: bar\n",
			want: []string{"\nfoo: bar\n"},
		},
		{
			name: "End with separator",
			data: "foo: bar\n---\n",
			want: []string{"foo: bar\n"},
		},
		{
			name: "Multiple separators",
			data: "doc1\n---\ndoc2\n---\ndoc3",
			want: []string{"doc1\n", "\ndoc2\n", "\ndoc3"},
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
