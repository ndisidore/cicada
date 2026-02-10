package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMatrixCombinations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		m    Matrix
		want []map[string]string
	}{
		{
			name: "single dimension",
			m: Matrix{
				Dimensions: []Dimension{
					{Name: "os", Values: []string{"linux", "darwin"}},
				},
			},
			want: []map[string]string{
				{"os": "linux"},
				{"os": "darwin"},
			},
		},
		{
			name: "multi dimension cartesian product",
			m: Matrix{
				Dimensions: []Dimension{
					{Name: "os", Values: []string{"linux", "darwin"}},
					{Name: "arch", Values: []string{"amd64", "arm64"}},
				},
			},
			want: []map[string]string{
				{"os": "linux", "arch": "amd64"},
				{"os": "linux", "arch": "arm64"},
				{"os": "darwin", "arch": "amd64"},
				{"os": "darwin", "arch": "arm64"},
			},
		},
		{
			name: "three dimensions",
			m: Matrix{
				Dimensions: []Dimension{
					{Name: "os", Values: []string{"linux"}},
					{Name: "go", Values: []string{"1.21", "1.22"}},
					{Name: "db", Values: []string{"pg", "mysql"}},
				},
			},
			want: []map[string]string{
				{"os": "linux", "go": "1.21", "db": "pg"},
				{"os": "linux", "go": "1.21", "db": "mysql"},
				{"os": "linux", "go": "1.22", "db": "pg"},
				{"os": "linux", "go": "1.22", "db": "mysql"},
			},
		},
		{
			name: "empty matrix",
			m:    Matrix{},
			want: nil,
		},
		{
			name: "single value dimension",
			m: Matrix{
				Dimensions: []Dimension{
					{Name: "os", Values: []string{"linux"}},
				},
			},
			want: []map[string]string{
				{"os": "linux"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.m.Combinations()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCollectImages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		p    Pipeline
		want []string
	}{
		{
			name: "unique images",
			p: Pipeline{
				Steps: []Step{
					{Image: "alpine:latest"},
					{Image: "golang:1.23"},
					{Image: "rust:1.76"},
				},
			},
			want: []string{"alpine:latest", "golang:1.23", "rust:1.76"},
		},
		{
			name: "deduplicates",
			p: Pipeline{
				Steps: []Step{
					{Image: "alpine:latest"},
					{Image: "alpine:latest"},
					{Image: "golang:1.23"},
				},
			},
			want: []string{"alpine:latest", "golang:1.23"},
		},
		{
			name: "empty pipeline",
			p:    Pipeline{},
			want: []string{},
		},
		{
			name: "single step",
			p: Pipeline{
				Steps: []Step{
					{Image: "ubuntu:22.04"},
				},
			},
			want: []string{"ubuntu:22.04"},
		},
		{
			name: "skips empty image refs",
			p: Pipeline{
				Steps: []Step{
					{Image: "alpine:latest"},
					{Image: ""},
					{Image: "golang:1.23"},
				},
			},
			want: []string{"alpine:latest", "golang:1.23"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := CollectImages(tt.p)
			assert.Equal(t, tt.want, got)
		})
	}
}
