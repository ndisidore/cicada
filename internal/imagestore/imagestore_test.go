package imagestore

import (
	"context"
	"errors"
	"testing"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndisidore/ciro/pkg/pipeline"
)

// fakeSolver implements the Solver interface for testing.
type fakeSolver struct {
	solveFn func(ctx context.Context, def *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error)
}

func (f *fakeSolver) Solve(ctx context.Context, def *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
	return f.solveFn(ctx, def, opt, ch)
}

// fakeDisplay implements progress.Display for testing.
type fakeDisplay struct{}

func (*fakeDisplay) Run(_ context.Context, _ string, ch <-chan *client.SolveStatus) error {
	for s := range ch {
		_ = s
	}
	return nil
}

func TestCollectImages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		p    pipeline.Pipeline
		want []string
	}{
		{
			name: "unique images",
			p: pipeline.Pipeline{
				Steps: []pipeline.Step{
					{Image: "alpine:latest"},
					{Image: "golang:1.23"},
					{Image: "rust:1.76"},
				},
			},
			want: []string{"alpine:latest", "golang:1.23", "rust:1.76"},
		},
		{
			name: "deduplicates",
			p: pipeline.Pipeline{
				Steps: []pipeline.Step{
					{Image: "alpine:latest"},
					{Image: "alpine:latest"},
					{Image: "golang:1.23"},
				},
			},
			want: []string{"alpine:latest", "golang:1.23"},
		},
		{
			name: "empty pipeline",
			p:    pipeline.Pipeline{},
			want: []string{},
		},
		{
			name: "single step",
			p: pipeline.Pipeline{
				Steps: []pipeline.Step{
					{Image: "ubuntu:22.04"},
				},
			},
			want: []string{"ubuntu:22.04"},
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

func TestCheckCached(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		images      []string
		solveFn     func(ctx context.Context, def *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error)
		wantMissing []string
		wantErr     bool
	}{
		{
			name:   "all cached",
			images: []string{"alpine:latest"},
			solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				ch <- &client.SolveStatus{
					Vertexes: []*client.Vertex{
						{Digest: digest.FromString("v1"), Cached: true},
					},
				}
				close(ch)
				return &client.SolveResponse{}, nil
			},
			wantMissing: []string{},
		},
		{
			name:   "not cached",
			images: []string{"alpine:latest"},
			solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				ch <- &client.SolveStatus{
					Vertexes: []*client.Vertex{
						{Digest: digest.FromString("v1"), Cached: false},
					},
				}
				close(ch)
				return &client.SolveResponse{}, nil
			},
			wantMissing: []string{"alpine:latest"},
		},
		{
			name:   "solve error propagates",
			images: []string{"alpine:latest"},
			solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				close(ch)
				return nil, errors.New("connection refused")
			},
			wantErr: true,
		},
		{
			name:   "no vertices treated as not cached",
			images: []string{"alpine:latest"},
			solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				close(ch)
				return &client.SolveResponse{}, nil
			},
			wantMissing: []string{"alpine:latest"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			solver := &fakeSolver{solveFn: tt.solveFn}
			missing, err := CheckCached(context.Background(), solver, tt.images)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantMissing, missing)
		})
	}
}

func TestPullImages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		images  []string
		solveFn func(ctx context.Context, def *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error)
		wantErr bool
	}{
		{
			name:   "successful pull",
			images: []string{"alpine:latest", "node:22"},
			solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				close(ch)
				return &client.SolveResponse{}, nil
			},
		},
		{
			name:   "pull failure",
			images: []string{"alpine:latest"},
			solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				close(ch)
				return nil, errors.New("pull failed")
			},
			wantErr: true,
		},
		{
			name:   "empty images is no-op",
			images: []string{},
			solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				close(ch)
				return &client.SolveResponse{}, nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			solver := &fakeSolver{solveFn: tt.solveFn}
			err := PullImages(context.Background(), solver, tt.images, &fakeDisplay{})
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
