package imagestore

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndisidore/cicada/internal/progress/progressmodel"
)

// fakeSolver implements the Solver interface for testing.
type fakeSolver struct {
	solveFn func(ctx context.Context, def *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error)
}

func (f *fakeSolver) Solve(ctx context.Context, def *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
	return f.solveFn(ctx, def, opt, ch)
}

// fakeSender implements progressmodel.Sender for testing by draining all messages.
type fakeSender struct{}

func (*fakeSender) Send(_ progressmodel.Msg) {}

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
			err := PullImages(t.Context(), solver, tt.images, &fakeSender{})
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}

	t.Run("pulls are concurrent", func(t *testing.T) {
		t.Parallel()

		// Barrier: both pulls must start before either can finish.
		// If pulls were sequential the first would block on <-allStarted forever.
		var started atomic.Int32
		allStarted := make(chan struct{})
		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			defer close(ch)
			if started.Add(1) == 2 {
				close(allStarted)
			}
			<-allStarted
			return &client.SolveResponse{}, nil
		}}

		err := PullImages(t.Context(), solver, []string{"a:latest", "b:latest"}, &fakeSender{})
		require.NoError(t, err)
	})

	t.Run("partial failure cancels in-flight pulls", func(t *testing.T) {
		t.Parallel()

		var firstCall atomic.Bool
		solver := &fakeSolver{solveFn: func(ctx context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			defer close(ch)
			if firstCall.CompareAndSwap(false, true) {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return nil, errors.New("pull failed")
		}}

		err := PullImages(t.Context(), solver, []string{"slow:latest", "fail:latest"}, &fakeSender{})
		require.Error(t, err)
	})

	t.Run("cancellation drains channel", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())

		solving := make(chan struct{})
		solver := &fakeSolver{solveFn: func(ctx context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			defer close(ch)
			close(solving)
			<-ctx.Done()
			// Post-cancellation writes the bridge must drain. Without the
			// ctx.Done() select in the bridge, a blocking Sender would
			// deadlock here because the bridge would call Send instead of
			// draining, and Solve could not close ch.
			for range 5 {
				ch <- &client.SolveStatus{}
			}
			return nil, ctx.Err()
		}}

		go func() {
			<-solving
			cancel()
		}()

		err := pullImage(ctx, solver, "test:latest", &fakeSender{})
		require.Error(t, err)
	})
}
