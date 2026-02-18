package imagestore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSolver implements the Solver interface for testing.
type fakeSolver struct {
	solveFn func(ctx context.Context, def *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error)
}

func (f *fakeSolver) Solve(ctx context.Context, def *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
	return f.solveFn(ctx, def, opt, ch)
}

// fakeDisplay implements progress.Display for testing.
type fakeDisplay struct {
	wg sync.WaitGroup
}

func (*fakeDisplay) Start(_ context.Context) error { return nil }

func (f *fakeDisplay) Attach(_ context.Context, _ string, ch <-chan *client.SolveStatus, _ map[string]time.Duration) error {
	f.wg.Go(func() {
		//revive:disable-next-line:empty-block // drain
		for range ch {
		}
	})
	return nil
}

func (*fakeDisplay) Skip(_ context.Context, _ string) {}

func (*fakeDisplay) SkipStep(_ context.Context, _, _ string) {}

func (*fakeDisplay) Retry(_ context.Context, _ string, _, _ int, _ error) {}

func (*fakeDisplay) Timeout(_ context.Context, _ string, _ time.Duration) {}

func (*fakeDisplay) Seal() {}

func (f *fakeDisplay) Wait() error { f.wg.Wait(); return nil }

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
			missing, err := CheckCached(t.Context(), solver, tt.images)
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
			d := &fakeDisplay{}
			err := PullImages(t.Context(), solver, tt.images, d)
			require.NoError(t, d.Wait())
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
