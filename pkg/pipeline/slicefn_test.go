package pipeline

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMapSlice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   []int
		fn   func(int) string
		want []string
	}{
		{
			name: "nil input returns nil",
			in:   nil,
			fn:   strconv.Itoa,
			want: nil,
		},
		{
			name: "empty non-nil input returns empty non-nil",
			in:   []int{},
			fn:   strconv.Itoa,
			want: []string{},
		},
		{
			name: "normal operation",
			in:   []int{1, 2, 3},
			fn:   strconv.Itoa,
			want: []string{"1", "2", "3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mapSlice(tt.in, tt.fn)
			assert.Equal(t, tt.want, got)
			if tt.in == nil {
				assert.Nil(t, got)
			}
			if tt.in != nil {
				assert.NotNil(t, got)
			}
		})
	}
}

func TestFlatMap(t *testing.T) {
	t.Parallel()

	split := func(s string) []string { return strings.Split(s, ",") }

	tests := []struct {
		name string
		in   []string
		fn   func(string) []string
		want []string
	}{
		{
			name: "nil input returns nil",
			in:   nil,
			fn:   split,
			want: nil,
		},
		{
			name: "empty non-nil input returns empty non-nil",
			in:   []string{},
			fn:   split,
			want: []string{},
		},
		{
			name: "normal operation",
			in:   []string{"a,b", "c"},
			fn:   split,
			want: []string{"a", "b", "c"},
		},
		{
			name: "all fn calls return empty slices",
			in:   []string{"x", "y"},
			fn:   func(string) []string { return nil },
			want: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := flatMap(tt.in, tt.fn)
			assert.Equal(t, tt.want, got)
			if tt.in == nil {
				assert.Nil(t, got)
			}
			if tt.in != nil {
				assert.NotNil(t, got)
			}
		})
	}
}
