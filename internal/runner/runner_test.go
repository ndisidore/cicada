package runner

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	gatewaypb "github.com/moby/buildkit/frontend/gateway/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"golang.org/x/sync/semaphore"

	"github.com/ndisidore/cicada/internal/cache"
	"github.com/ndisidore/cicada/internal/progress/progressmodel"
	rm "github.com/ndisidore/cicada/internal/runner/runnermodel"
	"github.com/ndisidore/cicada/internal/runtime/runtimetest"
	"github.com/ndisidore/cicada/pkg/conditional"
	"github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

// noopTracer returns a noop tracer for use in tests that construct runConfig directly.
func noopTracer() trace.Tracer {
	return tracenoop.NewTracerProvider().Tracer("cicada")
}

// fakeSolver implements the rm.Solver interface for testing.
type fakeSolver struct {
	solveFn func(ctx context.Context, def *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error)
	buildFn func(ctx context.Context, opt client.SolveOpt, product string, buildFunc gateway.BuildFunc, ch chan *client.SolveStatus) (*client.SolveResponse, error)
}

func (f *fakeSolver) Solve(ctx context.Context, def *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
	return f.solveFn(ctx, def, opt, ch)
}

func (f *fakeSolver) Build(ctx context.Context, opt client.SolveOpt, product string, buildFunc gateway.BuildFunc, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
	if f.buildFn != nil {
		return f.buildFn(ctx, opt, product, buildFunc, ch)
	}
	close(ch)
	return &client.SolveResponse{}, nil
}

// MockCondition is a testify mock for the rm.DeferredEvaluator interface.
type MockCondition struct {
	mock.Mock
}

func (m *MockCondition) EvaluateDeferred(ctx conditional.Context, depOutputs map[string]map[string]string) (bool, error) {
	args := m.Called(ctx, depOutputs)
	return args.Bool(0), args.Error(1)
}

// fakeGatewayClient implements gateway.Client for step-controlled tests.
// Only Solve is implemented; other methods panic.
type fakeGatewayClient struct {
	gateway.Client // embed for unimplemented methods
	solveFn        func(ctx context.Context, req gateway.SolveRequest) (*gateway.Result, error)
}

func (f *fakeGatewayClient) Solve(ctx context.Context, req gateway.SolveRequest) (*gateway.Result, error) {
	return f.solveFn(ctx, req)
}

// fakeSender implements progressmodel.Sender for testing.
type fakeSender struct {
	mu   sync.Mutex
	msgs []progressmodel.Msg
}

func (s *fakeSender) Send(msg progressmodel.Msg) {
	s.mu.Lock()
	s.msgs = append(s.msgs, msg)
	s.mu.Unlock()
}

// jobAddedOrder returns the ordered job names from JobAddedMsg messages.
func (s *fakeSender) jobAddedOrder() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var names []string
	for _, m := range s.msgs {
		if msg, ok := m.(progressmodel.JobAddedMsg); ok {
			names = append(names, msg.Job)
		}
	}
	return names
}

// hasJobAdded returns whether a JobAddedMsg was sent for the given job.
func (s *fakeSender) hasJobAdded(job string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.msgs {
		if msg, ok := m.(progressmodel.JobAddedMsg); ok && msg.Job == job {
			return true
		}
	}
	return false
}

// indexOf returns the position of s in slice, or -1 if not found.
func indexOf(slice []string, s string) int {
	for i, v := range slice {
		if v == s {
			return i
		}
	}
	return -1
}

func TestRun(t *testing.T) {
	t.Parallel()

	// Pre-marshal a minimal definition to reuse across test cases.
	def, err := llb.Scratch().Marshal(t.Context())
	require.NoError(t, err)

	cancelledCtx, cancel := context.WithCancel(t.Context())
	cancel()

	tests := []struct {
		name         string
		ctx          context.Context // if nil, t.Context() is used
		input        rm.RunInput
		wantErr      string
		wantSentinel error
	}{
		{
			name: "single job solves successfully",
			input: rm.RunInput{
				Solver: &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
					close(ch)
					return &client.SolveResponse{}, nil
				}},
				Jobs:   []rm.Job{{Name: "build", Definition: def}},
				Sender: &fakeSender{},
			},
		},
		{
			name: "multi-job execution",
			input: rm.RunInput{
				Solver: &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
					close(ch)
					return &client.SolveResponse{}, nil
				}},
				Jobs: []rm.Job{
					{Name: "first", Definition: def},
					{Name: "second", Definition: def},
					{Name: "third", Definition: def},
				},
				Sender: &fakeSender{},
			},
		},
		{
			name: "solve error wraps with job name",
			input: rm.RunInput{
				Solver: &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
					close(ch)
					return nil, errors.New("connection refused")
				}},
				Jobs:   []rm.Job{{Name: "deploy", Definition: def}},
				Sender: &fakeSender{},
			},
			wantErr: `job "deploy"`,
		},
		{
			name: "empty jobs is a no-op",
			input: rm.RunInput{
				Solver: &fakeSolver{},
				Jobs:   []rm.Job{},
				Sender: &fakeSender{},
			},
		},
		{
			name: "nil solver returns error",
			input: rm.RunInput{
				Solver: nil,
				Jobs:   []rm.Job{{Name: "x", Definition: def}},
				Sender: &fakeSender{},
			},
			wantSentinel: rm.ErrNilSolver,
		},
		{
			name: "nil sender returns error",
			input: rm.RunInput{
				Solver: &fakeSolver{},
				Jobs:   []rm.Job{{Name: "x", Definition: def}},
			},
			wantSentinel: rm.ErrNilSender,
		},
		{
			name: "nil runtime with export-docker returns error",
			input: rm.RunInput{
				Solver:  &fakeSolver{},
				Jobs:    []rm.Job{{Name: "x", Definition: def}},
				Sender:  &fakeSender{},
				Runtime: nil,
				ImagePublishes: []rm.ImagePublish{
					{ExportDocker: true, Image: "test:latest"},
				},
			},
			wantSentinel: rm.ErrNilRuntime,
		},
		{
			name: "unknown dependency",
			input: rm.RunInput{
				Solver: &fakeSolver{},
				Jobs:   []rm.Job{{Name: "a", Definition: def, DependsOn: []string{"nonexistent"}}},
				Sender: &fakeSender{},
			},
			wantSentinel: pipelinemodel.ErrUnknownDep,
		},
		{
			name: "duplicate job name",
			input: rm.RunInput{
				Solver: &fakeSolver{},
				Jobs: []rm.Job{
					{Name: "a", Definition: def},
					{Name: "a", Definition: def},
				},
				Sender: &fakeSender{},
			},
			wantSentinel: pipelinemodel.ErrDuplicateJob,
		},
		{
			name: "mutual cycle",
			input: rm.RunInput{
				Solver: &fakeSolver{},
				Jobs: []rm.Job{
					{Name: "a", Definition: def, DependsOn: []string{"b"}},
					{Name: "b", Definition: def, DependsOn: []string{"a"}},
				},
				Sender: &fakeSender{},
			},
			wantSentinel: pipelinemodel.ErrCycleDetected,
		},
		{
			name: "self cycle",
			input: rm.RunInput{
				Solver: &fakeSolver{},
				Jobs: []rm.Job{
					{Name: "a", Definition: def, DependsOn: []string{"a"}},
				},
				Sender: &fakeSender{},
			},
			wantSentinel: pipelinemodel.ErrCycleDetected,
		},
		{
			name: "nil definition returns error",
			input: rm.RunInput{
				Solver: &fakeSolver{},
				Jobs:   []rm.Job{{Name: "bad", Definition: nil}},
				Sender: &fakeSender{},
			},
			wantSentinel: rm.ErrNilDefinition,
		},
		{
			name: "context cancellation propagates",
			ctx:  cancelledCtx,
			input: rm.RunInput{
				Solver: &fakeSolver{solveFn: func(ctx context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
					close(ch)
					return nil, ctx.Err()
				}},
				Jobs:   []rm.Job{{Name: "cancelled", Definition: def}},
				Sender: &fakeSender{},
			},
			wantSentinel: context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := tt.ctx
			if ctx == nil {
				ctx = t.Context()
			}
			err := Run(ctx, tt.input)
			if tt.wantSentinel != nil {
				require.ErrorIs(t, err, tt.wantSentinel)
				return
			}
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestRun_ordering(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(t.Context())
	require.NoError(t, err)

	t.Run("linear chain", func(t *testing.T) {
		t.Parallel()

		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			return &client.SolveResponse{}, nil
		}}
		sender := &fakeSender{}

		// Chain a -> b -> c so they must run in order.
		err := Run(t.Context(), rm.RunInput{
			Solver: solver,
			Jobs: []rm.Job{
				{Name: "a", Definition: def},
				{Name: "b", Definition: def, DependsOn: []string{"a"}},
				{Name: "c", Definition: def, DependsOn: []string{"b"}},
			},
			Sender: sender,
		})
		require.NoError(t, err)

		order := sender.jobAddedOrder()
		require.Len(t, order, 3)
		idxA, idxB, idxC := indexOf(order, "a"), indexOf(order, "b"), indexOf(order, "c")
		require.NotEqual(t, -1, idxA, "a must appear in order")
		require.NotEqual(t, -1, idxB, "b must appear in order")
		require.NotEqual(t, -1, idxC, "c must appear in order")
		assert.Less(t, idxA, idxB, "a must run before b")
		assert.Less(t, idxB, idxC, "b must run before c")
	})

	t.Run("diamond", func(t *testing.T) {
		t.Parallel()

		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			return &client.SolveResponse{}, nil
		}}
		sender := &fakeSender{}

		// Diamond: a -> {b, c} -> d
		err := Run(t.Context(), rm.RunInput{
			Solver: solver,
			Jobs: []rm.Job{
				{Name: "a", Definition: def},
				{Name: "b", Definition: def, DependsOn: []string{"a"}},
				{Name: "c", Definition: def, DependsOn: []string{"a"}},
				{Name: "d", Definition: def, DependsOn: []string{"b", "c"}},
			},
			Sender: sender,
		})
		require.NoError(t, err)

		order := sender.jobAddedOrder()
		require.Len(t, order, 4)
		idxA := indexOf(order, "a")
		idxB := indexOf(order, "b")
		idxC := indexOf(order, "c")
		idxD := indexOf(order, "d")
		assert.Less(t, idxA, idxB, "a must run before b")
		assert.Less(t, idxA, idxC, "a must run before c")
		assert.Less(t, idxB, idxD, "b must run before d")
		assert.Less(t, idxC, idxD, "c must run before d")
	})
}

func TestRun_parallelism(t *testing.T) {
	t.Parallel()

	t.Run("independent jobs run concurrently", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			def, err := llb.Scratch().Marshal(t.Context())
			require.NoError(t, err)

			var concurrent, maxConcurrent atomic.Int64

			solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				cur := concurrent.Add(1)
				for {
					old := maxConcurrent.Load()
					if cur <= old || maxConcurrent.CompareAndSwap(old, cur) {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
				concurrent.Add(-1)
				close(ch)
				return &client.SolveResponse{}, nil
			}}

			err = Run(t.Context(), rm.RunInput{
				Solver: solver,
				Jobs: []rm.Job{
					{Name: "a", Definition: def},
					{Name: "b", Definition: def},
					{Name: "c", Definition: def},
				},
				Sender: &fakeSender{},
			})
			require.NoError(t, err)
			assert.Equal(t, int64(3), maxConcurrent.Load(), "all 3 independent jobs should run concurrently")
		})
	})

	t.Run("bounded by parallelism flag", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			def, err := llb.Scratch().Marshal(t.Context())
			require.NoError(t, err)

			var concurrent, maxConcurrent atomic.Int64

			solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				cur := concurrent.Add(1)
				for {
					old := maxConcurrent.Load()
					if cur <= old || maxConcurrent.CompareAndSwap(old, cur) {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
				concurrent.Add(-1)
				close(ch)
				return &client.SolveResponse{}, nil
			}}

			err = Run(t.Context(), rm.RunInput{
				Solver: solver,
				Jobs: []rm.Job{
					{Name: "a", Definition: def},
					{Name: "b", Definition: def},
					{Name: "c", Definition: def},
					{Name: "d", Definition: def},
				},
				Sender:      &fakeSender{},
				Parallelism: 2,
			})
			require.NoError(t, err)
			assert.LessOrEqual(t, maxConcurrent.Load(), int64(2), "should not exceed parallelism=2")
		})
	})
}

func TestRun_errorPropagation(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(t.Context())
	require.NoError(t, err)

	t.Run("error cancels downstream", func(t *testing.T) {
		t.Parallel()

		// Separate definitions so the solver can identify each job
		// by pointer, without relying on display side-channel state.
		defA, err := llb.Scratch().Marshal(t.Context())
		require.NoError(t, err)
		defB, err := llb.Scratch().Marshal(t.Context())
		require.NoError(t, err)
		defC, err := llb.Scratch().Marshal(t.Context())
		require.NoError(t, err)

		sender := &fakeSender{}

		// "a" succeeds, "b" fails at solve, "c" depends on "b".
		solver := &fakeSolver{solveFn: func(_ context.Context, def *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			if def == defB {
				return nil, errors.New("job b exploded")
			}
			return &client.SolveResponse{}, nil
		}}

		err = Run(t.Context(), rm.RunInput{
			Solver: solver,
			Jobs: []rm.Job{
				{Name: "a", Definition: defA},
				{Name: "b", Definition: defB, DependsOn: []string{"a"}},
				{Name: "c", Definition: defC, DependsOn: []string{"b"}},
			},
			Sender: sender,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "job b exploded")
		assert.False(t, sender.hasJobAdded("c"), "job c should never execute when dep b fails")
	})

	t.Run("propagates to grandchild", func(t *testing.T) {
		t.Parallel()

		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			return nil, errors.New("job a failed")
		}}
		sender := &fakeSender{}

		// Chain a -> b -> c. Job "a" fails; "c" should never be solved.
		err := Run(t.Context(), rm.RunInput{
			Solver: solver,
			Jobs: []rm.Job{
				{Name: "a", Definition: def},
				{Name: "b", Definition: def, DependsOn: []string{"a"}},
				{Name: "c", Definition: def, DependsOn: []string{"b"}},
			},
			Sender:      sender,
			Parallelism: 1,
		})
		require.Error(t, err)
		assert.False(t, sender.hasJobAdded("c"), "job c should never be solved when grandparent a fails")
	})

	t.Run("failed job unblocks deps", func(t *testing.T) {
		t.Parallel()

		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			return nil, errors.New("job a failed")
		}}

		sender := &fakeSender{}

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		err := Run(ctx, rm.RunInput{
			Solver: solver,
			Jobs: []rm.Job{
				{Name: "a", Definition: def},
				{Name: "b", Definition: def, DependsOn: []string{"a"}},
			},
			Sender: sender,
		})
		require.Error(t, err)
		// Must not be a context deadline exceeded (that would mean it hung).
		assert.NotErrorIs(t, err, context.DeadlineExceeded)
	})

	t.Run("dep error skips solve", func(t *testing.T) {
		t.Parallel()

		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			return nil, errors.New("job a failed")
		}}
		sender := &fakeSender{}

		err := Run(t.Context(), rm.RunInput{
			Solver: solver,
			Jobs: []rm.Job{
				{Name: "a", Definition: def},
				{Name: "b", Definition: def, DependsOn: []string{"a"}},
			},
			Sender:      sender,
			Parallelism: 1,
		})
		require.Error(t, err)
		assert.False(t, sender.hasJobAdded("b"), "job b's solve should never be invoked when dep a fails")
	})
}

func TestRun_display(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(t.Context())
	require.NoError(t, err)

	t.Run("solver writes status events", func(t *testing.T) {
		t.Parallel()

		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			ch <- &client.SolveStatus{}
			ch <- &client.SolveStatus{}
			close(ch)
			return &client.SolveResponse{}, nil
		}}
		sender := &fakeSender{}

		err := Run(t.Context(), rm.RunInput{
			Solver: solver,
			Jobs:   []rm.Job{{Name: "status-test", Definition: def}},
			Sender: sender,
		})
		require.NoError(t, err)

		// Lock manually because we need to filter by type, not just count.
		var statusCount int
		sender.mu.Lock()
		for _, m := range sender.msgs {
			if _, ok := m.(progressmodel.JobStatusMsg); ok {
				statusCount++
			}
		}
		sender.mu.Unlock()
		assert.Equal(t, 2, statusCount)
	})
}

func TestRun_semAcquireFailurePropagates(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(t.Context())
	require.NoError(t, err)

	var solved atomic.Bool
	solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
		solved.Store(true)
		close(ch)
		return &client.SolveResponse{}, nil
	}}

	// Pre-cancel so sem.Acquire fails immediately for all jobs.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err = Run(ctx, rm.RunInput{
		Solver: solver,
		Jobs: []rm.Job{
			{Name: "a", Definition: def},
			{Name: "b", Definition: def, DependsOn: []string{"a"}},
		},
		Sender:      &fakeSender{},
		Parallelism: 1,
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, solved.Load(), "solver should not be called when sem.Acquire fails")
}

func TestRun_exports(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(t.Context())
	require.NoError(t, err)

	tests := []struct {
		name          string
		exports       []rm.Export
		solver        *fakeSolver
		sender        *fakeSender
		wantErr       string
		wantSentinel  error
		checkOpt      bool   // whether to assert on capturedExportOpt after Run
		wantOutputDir string // expected OutputDir when checkOpt is true
	}{
		{
			name: "export solves with local exporter",
			exports: []rm.Export{
				{Definition: def, JobName: "build", Local: "/tmp/out/myapp"},
			},
			solver: &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				close(ch)
				return &client.SolveResponse{}, nil
			}},
			sender:        &fakeSender{},
			checkOpt:      true,
			wantOutputDir: "/tmp/out",
		},
		{
			name: "directory export uses Local as OutputDir",
			exports: []rm.Export{
				{Definition: def, JobName: "build", Local: "/tmp/out/dist", Dir: true},
			},
			solver: &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				close(ch)
				return &client.SolveResponse{}, nil
			}},
			sender:        &fakeSender{},
			checkOpt:      true,
			wantOutputDir: "/tmp/out/dist",
		},
		{
			name: "export solve error wraps job and path",
			exports: []rm.Export{
				{Definition: def, JobName: "compile", Local: "/tmp/bin/app"},
			},
			solver: &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				close(ch)
				return nil, errors.New("disk full")
			}},
			sender:  &fakeSender{},
			wantErr: `exporting "/tmp/bin/app" from job "compile"`,
		},
		{
			name: "nil export definition returns error",
			exports: []rm.Export{
				{Definition: nil, JobName: "bad", Local: "/tmp/out/x"},
			},
			solver:       &fakeSolver{},
			sender:       &fakeSender{},
			wantSentinel: rm.ErrNilDefinition,
		},
		{
			name: "empty export local returns error",
			exports: []rm.Export{
				{Definition: def, JobName: "bad", Local: ""},
			},
			solver:       &fakeSolver{},
			sender:       &fakeSender{},
			wantSentinel: pipelinemodel.ErrEmptyExportLocal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var capturedExportOpt atomic.Pointer[client.SolveOpt]

			// Use the same solver for both the job and export phases.
			// For job phase, always succeed.
			jobSolver := &fakeSolver{solveFn: func(ctx context.Context, d *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				// Export phase: capture opt and delegate to test's solver.
				if len(opt.Exports) > 0 {
					capturedExportOpt.Store(&opt)
					return tt.solver.solveFn(ctx, d, opt, ch)
				}
				close(ch)
				return &client.SolveResponse{}, nil
			}}

			err := Run(t.Context(), rm.RunInput{
				Solver:  jobSolver,
				Jobs:    []rm.Job{{Name: "job", Definition: def}},
				Sender:  tt.sender,
				Exports: tt.exports,
			})
			if tt.wantSentinel != nil {
				require.ErrorIs(t, err, tt.wantSentinel)
				return
			}
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.checkOpt {
				opt := capturedExportOpt.Load()
				require.NotNil(t, opt)
				require.Len(t, opt.Exports, 1)
				assert.Equal(t, client.ExporterLocal, opt.Exports[0].Type)
				assert.Equal(t, tt.wantOutputDir, opt.Exports[0].OutputDir)
			}
		})
	}

	t.Run("concurrent exports", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			def, err := llb.Scratch().Marshal(t.Context())
			require.NoError(t, err)

			var concurrent, maxConcurrent atomic.Int64

			solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				// Only track concurrency for export solves.
				if len(opt.Exports) > 0 {
					cur := concurrent.Add(1)
					for {
						old := maxConcurrent.Load()
						if cur <= old || maxConcurrent.CompareAndSwap(old, cur) {
							break
						}
					}
					time.Sleep(10 * time.Millisecond)
					concurrent.Add(-1)
				}
				close(ch)
				return &client.SolveResponse{}, nil
			}}

			err = Run(t.Context(), rm.RunInput{
				Solver: solver,
				Jobs:   []rm.Job{{Name: "build", Definition: def}},
				Sender: &fakeSender{},
				Exports: []rm.Export{
					{Definition: def, JobName: "build", Local: "/tmp/a"},
					{Definition: def, JobName: "build", Local: "/tmp/b"},
					{Definition: def, JobName: "build", Local: "/tmp/c"},
				},
			})
			require.NoError(t, err)
			assert.Equal(t, int64(3), maxConcurrent.Load(), "all 3 exports should run concurrently")
		})
	})

	t.Run("error cancels siblings", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			def, err := llb.Scratch().Marshal(t.Context())
			require.NoError(t, err)

			var started atomic.Int64

			solver := &fakeSolver{solveFn: func(ctx context.Context, _ *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				if len(opt.Exports) > 0 {
					idx := started.Add(1)
					if idx == 1 {
						close(ch)
						return nil, errors.New("disk full")
					}
					// Other exports block until cancelled.
					<-ctx.Done()
					close(ch)
					return nil, ctx.Err()
				}
				close(ch)
				return &client.SolveResponse{}, nil
			}}

			err = Run(t.Context(), rm.RunInput{
				Solver: solver,
				Jobs:   []rm.Job{{Name: "build", Definition: def}},
				Sender: &fakeSender{},
				Exports: []rm.Export{
					{Definition: def, JobName: "build", Local: "/tmp/a"},
					{Definition: def, JobName: "build", Local: "/tmp/b"},
				},
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "disk full")
		})
	})
}

func TestRun_cacheEntriesFlowToSolveOpt(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(t.Context())
	require.NoError(t, err)

	exports := []client.CacheOptionsEntry{{Type: "registry", Attrs: map[string]string{"ref": "ghcr.io/org/cache"}}}
	imports := []client.CacheOptionsEntry{{Type: "local", Attrs: map[string]string{"src": "/tmp/cache"}}}

	var capturedOpt atomic.Pointer[client.SolveOpt]
	solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
		capturedOpt.Store(&opt)
		close(ch)
		return &client.SolveResponse{}, nil
	}}

	err = Run(t.Context(), rm.RunInput{
		Solver:       solver,
		Jobs:         []rm.Job{{Name: "build", Definition: def}},
		Sender:       &fakeSender{},
		CacheExports: exports,
		CacheImports: imports,
	})
	require.NoError(t, err)

	opt := capturedOpt.Load()
	require.NotNil(t, opt)
	assert.Equal(t, exports, opt.CacheExports)
	assert.Equal(t, imports, opt.CacheImports)
}

func TestRun_imagePublish(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(t.Context())
	require.NoError(t, err)

	t.Run("single publish uses image exporter", func(t *testing.T) {
		t.Parallel()

		var capturedOpt atomic.Pointer[client.SolveOpt]
		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			if len(opt.Exports) > 0 {
				capturedOpt.Store(&opt)
			}
			close(ch)
			return &client.SolveResponse{}, nil
		}}

		err := Run(t.Context(), rm.RunInput{
			Solver: solver,
			Jobs:   []rm.Job{{Name: "build", Definition: def}},
			Sender: &fakeSender{},
			ImagePublishes: []rm.ImagePublish{
				{Definition: def, JobName: "build", Image: "ghcr.io/user/app:v1", Push: true, Platform: "linux/amd64"},
			},
		})
		require.NoError(t, err)

		opt := capturedOpt.Load()
		require.NotNil(t, opt)
		require.Len(t, opt.Exports, 1)
		assert.Equal(t, client.ExporterImage, opt.Exports[0].Type)
		assert.Equal(t, "ghcr.io/user/app:v1", opt.Exports[0].Attrs["name"])
		assert.Equal(t, "true", opt.Exports[0].Attrs["push"])
	})

	t.Run("publish with insecure sets registry.insecure", func(t *testing.T) {
		t.Parallel()

		var capturedOpt atomic.Pointer[client.SolveOpt]
		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			if len(opt.Exports) > 0 {
				capturedOpt.Store(&opt)
			}
			close(ch)
			return &client.SolveResponse{}, nil
		}}

		err := Run(t.Context(), rm.RunInput{
			Solver: solver,
			Jobs:   []rm.Job{{Name: "build", Definition: def}},
			Sender: &fakeSender{},
			ImagePublishes: []rm.ImagePublish{
				{Definition: def, JobName: "build", Image: "localhost:5000/app:dev", Push: true, Insecure: true, Platform: "linux/amd64"},
			},
		})
		require.NoError(t, err)

		opt := capturedOpt.Load()
		require.NotNil(t, opt)
		assert.Equal(t, "true", opt.Exports[0].Attrs["registry.insecure"])
	})

	t.Run("publish with push=false omits push attr", func(t *testing.T) {
		t.Parallel()

		var capturedOpt atomic.Pointer[client.SolveOpt]
		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			if len(opt.Exports) > 0 {
				capturedOpt.Store(&opt)
			}
			close(ch)
			return &client.SolveResponse{}, nil
		}}

		err := Run(t.Context(), rm.RunInput{
			Solver: solver,
			Jobs:   []rm.Job{{Name: "build", Definition: def}},
			Sender: &fakeSender{},
			ImagePublishes: []rm.ImagePublish{
				{Definition: def, JobName: "build", Image: "myapp:dev", Push: false, Platform: "linux/amd64"},
			},
		})
		require.NoError(t, err)

		opt := capturedOpt.Load()
		require.NotNil(t, opt)
		_, hasPush := opt.Exports[0].Attrs["push"]
		assert.False(t, hasPush, "push attr should not be set when Push=false")
	})

	t.Run("publish error wraps image and job name", func(t *testing.T) {
		t.Parallel()

		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			if len(opt.Exports) > 0 {
				return nil, errors.New("registry unavailable")
			}
			return &client.SolveResponse{}, nil
		}}

		err := Run(t.Context(), rm.RunInput{
			Solver: solver,
			Jobs:   []rm.Job{{Name: "build", Definition: def}},
			Sender: &fakeSender{},
			ImagePublishes: []rm.ImagePublish{
				{Definition: def, JobName: "build", Image: "ghcr.io/user/app:v1", Push: true, Platform: "linux/amd64"},
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ghcr.io/user/app:v1")
		assert.Contains(t, err.Error(), "build")
	})

	t.Run("nil publish definition returns error", func(t *testing.T) {
		t.Parallel()

		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			return &client.SolveResponse{}, nil
		}}

		err := Run(t.Context(), rm.RunInput{
			Solver: solver,
			Jobs:   []rm.Job{{Name: "build", Definition: def}},
			Sender: &fakeSender{},
			ImagePublishes: []rm.ImagePublish{
				{Definition: nil, JobName: "build", Image: "ghcr.io/user/app:v1", Push: true},
			},
		})
		require.ErrorIs(t, err, rm.ErrNilDefinition)
	})
}

func TestGroupPublishes(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(t.Context())
	require.NoError(t, err)

	t.Run("single variant produces one group", func(t *testing.T) {
		t.Parallel()

		pubs := []rm.ImagePublish{
			{Definition: def, JobName: "build", Image: "ghcr.io/user/app:v1", Push: true, Platform: "linux/amd64"},
		}
		groups, err := groupPublishes(pubs)
		require.NoError(t, err)
		require.Len(t, groups, 1)
		assert.Equal(t, "ghcr.io/user/app:v1", groups[0].Image)
		assert.True(t, groups[0].Push)
		require.Len(t, groups[0].Variants, 1)
	})

	t.Run("same image groups together", func(t *testing.T) {
		t.Parallel()

		pubs := []rm.ImagePublish{
			{Definition: def, JobName: "build-amd64", Image: "ghcr.io/user/app:v1", Push: true, Platform: "linux/amd64"},
			{Definition: def, JobName: "build-arm64", Image: "ghcr.io/user/app:v1", Push: true, Platform: "linux/arm64"},
		}
		groups, err := groupPublishes(pubs)
		require.NoError(t, err)
		require.Len(t, groups, 1)
		assert.Equal(t, "ghcr.io/user/app:v1", groups[0].Image)
		require.Len(t, groups[0].Variants, 2)
	})

	t.Run("different images separate groups", func(t *testing.T) {
		t.Parallel()

		pubs := []rm.ImagePublish{
			{Definition: def, JobName: "a", Image: "app:v1", Push: true, Platform: "linux/amd64"},
			{Definition: def, JobName: "b", Image: "app:v2", Push: true, Platform: "linux/amd64"},
		}
		groups, err := groupPublishes(pubs)
		require.NoError(t, err)
		require.Len(t, groups, 2)
		assert.Equal(t, "app:v1", groups[0].Image)
		assert.Equal(t, "app:v2", groups[1].Image)
	})

	t.Run("conflicting push setting rejected", func(t *testing.T) {
		t.Parallel()

		pubs := []rm.ImagePublish{
			{Definition: def, JobName: "build-amd64", Image: "ghcr.io/user/app:v1", Push: true, Platform: "linux/amd64"},
			{Definition: def, JobName: "build-arm64", Image: "ghcr.io/user/app:v1", Push: false, Platform: "linux/arm64"},
		}
		_, err := groupPublishes(pubs)
		require.ErrorIs(t, err, rm.ErrPublishSettingConflict)
	})

	t.Run("conflicting export-docker setting rejected", func(t *testing.T) {
		t.Parallel()

		pubs := []rm.ImagePublish{
			{Definition: def, JobName: "build-amd64", Image: "app:v1", ExportDocker: true, Platform: "linux/amd64"},
			{Definition: def, JobName: "build-arm64", Image: "app:v1", ExportDocker: false, Platform: "linux/arm64"},
		}
		_, err := groupPublishes(pubs)
		require.ErrorIs(t, err, rm.ErrPublishSettingConflict)
	})
}

func TestGroupPublishes_exportDocker(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(t.Context())
	require.NoError(t, err)

	pubs := []rm.ImagePublish{
		{Definition: def, JobName: "build", Image: "myapp:dev", ExportDocker: true, Platform: "linux/amd64"},
	}
	groups, err := groupPublishes(pubs)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	assert.True(t, groups[0].ExportDocker)
}

func TestRun_exportDockerMultiPlatformError(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(t.Context())
	require.NoError(t, err)

	solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
		close(ch)
		return &client.SolveResponse{}, nil
	}}

	rt := new(runtimetest.MockRuntime)
	err = Run(t.Context(), rm.RunInput{
		Solver:  solver,
		Runtime: rt,
		Jobs:    []rm.Job{{Name: "build", Definition: def}},
		Sender:  &fakeSender{},
		ImagePublishes: []rm.ImagePublish{
			{Definition: def, JobName: "build-amd64", Image: "myapp:latest", ExportDocker: true, Platform: "linux/amd64"},
			{Definition: def, JobName: "build-arm64", Image: "myapp:latest", ExportDocker: true, Platform: "linux/arm64"},
		},
	})
	require.ErrorIs(t, err, rm.ErrExportDockerMultiPlatform)
	rt.AssertNotCalled(t, "LoadImage")
}

func TestRun_duplicatePlatformError(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(t.Context())
	require.NoError(t, err)

	solver := &fakeSolver{
		solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			return &client.SolveResponse{}, nil
		},
		buildFn: func(ctx context.Context, opt client.SolveOpt, product string, buildFunc gateway.BuildFunc, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			defer close(ch)
			// Invoke the build function so multiPlatformBuildFunc runs its pre-check.
			_, err := buildFunc(ctx, nil)
			return &client.SolveResponse{}, err
		},
	}

	err = Run(t.Context(), rm.RunInput{
		Solver: solver,
		Jobs:   []rm.Job{{Name: "build", Definition: def}},
		Sender: &fakeSender{},
		ImagePublishes: []rm.ImagePublish{
			{Definition: def, JobName: "build-a", Image: "myapp:latest", Push: true, Platform: "linux/amd64"},
			{Definition: def, JobName: "build-b", Image: "myapp:latest", Push: true, Platform: "linux/amd64"},
		},
	})
	require.ErrorIs(t, err, rm.ErrDuplicatePlatform)
}

// TestRun_exportDocker tests the export-docker path using a mock runtime.
func TestRun_exportDocker(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(t.Context())
	require.NoError(t, err)

	t.Run("solves with docker exporter and Output set", func(t *testing.T) {
		t.Parallel()

		rt := new(runtimetest.MockRuntime)
		rt.On("LoadImage", mock.Anything).Return(nil)

		var capturedOpt atomic.Pointer[client.SolveOpt]
		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			if len(opt.Exports) > 0 && opt.Exports[0].Type == client.ExporterDocker {
				capturedOpt.Store(&opt)
			}
			close(ch)
			return &client.SolveResponse{}, nil
		}}

		err := Run(t.Context(), rm.RunInput{
			Solver:  solver,
			Runtime: rt,
			Jobs:    []rm.Job{{Name: "build", Definition: def}},
			Sender:  &fakeSender{},
			ImagePublishes: []rm.ImagePublish{
				{Definition: def, JobName: "build", Image: "myapp:dev", ExportDocker: true, Platform: "linux/amd64"},
			},
		})
		require.NoError(t, err)

		opt := capturedOpt.Load()
		require.NotNil(t, opt, "docker exporter solve should have been called")
		require.Len(t, opt.Exports, 1)
		assert.Equal(t, client.ExporterDocker, opt.Exports[0].Type)
		assert.Equal(t, "myapp:dev", opt.Exports[0].Attrs["name"])
		assert.NotNil(t, opt.Exports[0].Output, "Output callback must be set for docker exporter")
		rt.AssertExpectations(t)
	})

	t.Run("push and export-docker run concurrently", func(t *testing.T) {
		t.Parallel()

		rt := new(runtimetest.MockRuntime)
		rt.On("LoadImage", mock.Anything).Return(nil)

		var imageExporterCalled, dockerExporterCalled atomic.Bool
		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			if len(opt.Exports) > 0 {
				switch opt.Exports[0].Type {
				case client.ExporterImage:
					imageExporterCalled.Store(true)
				case client.ExporterDocker:
					dockerExporterCalled.Store(true)
				default:
				}
			}
			close(ch)
			return &client.SolveResponse{}, nil
		}}

		err := Run(t.Context(), rm.RunInput{
			Solver:  solver,
			Runtime: rt,
			Jobs:    []rm.Job{{Name: "build", Definition: def}},
			Sender:  &fakeSender{},
			ImagePublishes: []rm.ImagePublish{
				{Definition: def, JobName: "build", Image: "ghcr.io/user/app:v1", Push: true, ExportDocker: true, Platform: "linux/amd64"},
			},
		})
		require.NoError(t, err)
		assert.True(t, imageExporterCalled.Load(), "image exporter should be called for push")
		assert.True(t, dockerExporterCalled.Load(), "docker exporter should be called for export-docker")
		rt.AssertExpectations(t)
	})

	t.Run("nil definition returns error", func(t *testing.T) {
		t.Parallel()

		rt := new(runtimetest.MockRuntime)
		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			return &client.SolveResponse{}, nil
		}}

		err := Run(t.Context(), rm.RunInput{
			Solver:  solver,
			Runtime: rt,
			Jobs:    []rm.Job{{Name: "build", Definition: def}},
			Sender:  &fakeSender{},
			ImagePublishes: []rm.ImagePublish{
				{Definition: nil, JobName: "build", Image: "myapp:dev", ExportDocker: true, Platform: "linux/amd64"},
			},
		})
		require.ErrorIs(t, err, rm.ErrNilDefinition)
		rt.AssertNotCalled(t, "LoadImage")
	})
}

func TestTeeStatus(t *testing.T) {
	t.Parallel()

	t.Run("nil collector returns source", func(t *testing.T) {
		t.Parallel()

		ch := make(chan *client.SolveStatus, 1)
		result := teeStatus(t.Context(), teeStatusInput{src: ch, jobName: "step"})
		// With nil collector, teeStatus returns the source channel directly.
		assert.Equal(t, (<-chan *client.SolveStatus)(ch), result)
	})

	t.Run("forwards and observes", func(t *testing.T) {
		t.Parallel()

		now := time.Now()
		started := now.Add(-time.Second)
		completed := now

		collector := cache.NewCollector()
		src := make(chan *client.SolveStatus, 2)
		src <- &client.SolveStatus{
			Vertexes: []*client.Vertex{
				{Digest: digest.FromString("v1"), Name: "op1", Started: &started, Completed: &completed, Cached: true},
			},
		}
		src <- &client.SolveStatus{
			Vertexes: []*client.Vertex{
				{Digest: digest.FromString("v2"), Name: "op2", Started: &started, Completed: &completed, Cached: false},
			},
		}
		close(src)

		out := teeStatus(t.Context(), teeStatusInput{src: src, collector: collector, jobName: "build"})

		var received int
		for range out {
			received++
		}
		assert.Equal(t, 2, received)

		r := collector.Report()
		require.Len(t, r.Jobs, 1)
		assert.Equal(t, 2, r.Jobs[0].TotalOps)
		assert.Equal(t, 1, r.Jobs[0].CachedOps)
	})

	t.Run("context cancellation", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			collector := cache.NewCollector()
			src := make(chan *client.SolveStatus)

			ctx, cancel := context.WithCancel(t.Context())
			out := teeStatus(ctx, teeStatusInput{src: src, collector: collector, jobName: "step"})

			// Cancel; the tee goroutine exits the select loop and drains src.
			cancel()
			// Simulate the Solve goroutine closing src after context cancellation.
			close(src)

			// Drain out; should terminate within the synctest bubble.
			//revive:disable-next-line:empty-block // drain remaining events
			for range out {
			}
		})
	})
}

func TestParseOutputLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    map[string]string
	}{
		{
			name:    "simple key-value",
			content: "STATUS=ok\nBUILD=true\n",
			want:    map[string]string{"STATUS": "ok", "BUILD": "true"},
		},
		{
			name:    "empty content",
			content: "",
			want:    map[string]string{},
		},
		{
			name:    "blank lines skipped",
			content: "\nKEY=val\n\n",
			want:    map[string]string{"KEY": "val"},
		},
		{
			name:    "lines without equals skipped",
			content: "invalid-line\nKEY=val\n",
			want:    map[string]string{"KEY": "val"},
		},
		{
			name:    "value contains equals",
			content: "URL=https://example.com?a=1&b=2\n",
			want:    map[string]string{"URL": "https://example.com?a=1&b=2"},
		},
		{
			name:    "whitespace trimmed",
			content: "  KEY=val  \n",
			want:    map[string]string{"KEY": "val"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseOutputLines(tt.content)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCollectDepOutputs(t *testing.T) {
	t.Parallel()

	nodes := map[string]*dagNode{
		"a": {outputs: map[string]string{"status": "ok"}},
		"b": {outputs: nil},
		"c": {outputs: map[string]string{"ready": "yes"}},
	}

	result := collectDepOutputs(nodes, []string{"a", "b", "c"})
	assert.Equal(t, map[string]string{"status": "ok"}, result["a"])
	assert.Nil(t, result["b"])
	assert.Equal(t, map[string]string{"ready": "yes"}, result["c"])
}

func TestRunNodeDeferredWhenSkip(t *testing.T) {
	t.Parallel()

	solver := &fakeSolver{
		solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			return &client.SolveResponse{}, nil
		},
	}

	sender := &fakeSender{}

	wctx := &conditional.Context{
		Getenv:      func(string) string { return "" },
		Branch:      "develop",
		Tag:         "",
		PipelineEnv: map[string]string{},
	}

	depNode := &dagNode{
		job:     rm.Job{Name: "build", Definition: &llb.Definition{Def: [][]byte{{}}}},
		done:    make(chan struct{}),
		outputs: map[string]string{"deploy": "no"},
	}
	close(depNode.done)

	whenMock := &MockCondition{}
	whenMock.On("EvaluateDeferred", mock.Anything, mock.Anything).Return(false, nil)

	node := &dagNode{
		job: rm.Job{
			Name:       "deploy",
			Definition: &llb.Definition{Def: [][]byte{{}}},
			DependsOn:  []string{"build"},
			When:       whenMock,
			Env:        map[string]string{},
			Matrix:     map[string]string{},
		},
		done: make(chan struct{}),
	}

	nodes := map[string]*dagNode{
		"build":  depNode,
		"deploy": node,
	}

	cfg := runConfig{
		solver:  solver,
		sender:  sender,
		nodes:   nodes,
		sem:     semaphore.NewWeighted(1),
		whenCtx: wctx,
		tracer:  noopTracer(),
	}

	err := runNode(t.Context(), node, cfg)
	require.NoError(t, err)
	assert.True(t, node.skipped)

	// Verify a JobSkippedMsg was sent for "deploy".
	var skippedJob string
	for _, m := range sender.msgs {
		if msg, ok := m.(progressmodel.JobSkippedMsg); ok {
			skippedJob = msg.Job
		}
	}
	assert.Equal(t, "deploy", skippedJob)
	whenMock.AssertExpectations(t)
}

func TestRunNodeSkippedDepPropagatesSkip(t *testing.T) {
	t.Parallel()

	solver := &fakeSolver{
		solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			return &client.SolveResponse{}, nil
		},
	}

	sender := &fakeSender{}

	depNode := &dagNode{
		job:     rm.Job{Name: "build", Definition: &llb.Definition{Def: [][]byte{{}}}},
		done:    make(chan struct{}),
		skipped: true,
	}
	close(depNode.done)

	// Downstream node has a deferred When that should never be called
	// because its dependency was skipped.
	whenMock := &MockCondition{}

	node := &dagNode{
		job: rm.Job{
			Name:       "deploy",
			Definition: &llb.Definition{Def: [][]byte{{}}},
			DependsOn:  []string{"build"},
			When:       whenMock,
			Env:        map[string]string{},
			Matrix:     map[string]string{},
		},
		done: make(chan struct{}),
	}

	nodes := map[string]*dagNode{
		"build":  depNode,
		"deploy": node,
	}

	cfg := runConfig{
		solver: solver,
		sender: sender,
		nodes:  nodes,
		sem:    semaphore.NewWeighted(1),
		tracer: noopTracer(),
		whenCtx: &conditional.Context{
			Getenv:      func(string) string { return "" },
			PipelineEnv: map[string]string{},
		},
	}

	err := runNode(t.Context(), node, cfg)
	require.NoError(t, err)
	assert.True(t, node.skipped)

	// Verify a JobSkippedMsg was sent for "deploy".
	var skippedJobs []string
	for _, m := range sender.msgs {
		if msg, ok := m.(progressmodel.JobSkippedMsg); ok {
			skippedJobs = append(skippedJobs, msg.Job)
		}
	}
	assert.Equal(t, []string{"deploy"}, skippedJobs)
	// EvaluateDeferred must not be called when dep is skipped.
	whenMock.AssertNotCalled(t, "EvaluateDeferred", mock.Anything, mock.Anything)
}

func TestRunNodeSkippedStepsReported(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(t.Context())
	require.NoError(t, err)

	solver := &fakeSolver{
		solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			return &client.SolveResponse{}, nil
		},
	}

	sender := &fakeSender{}

	node := &dagNode{
		job: rm.Job{
			Name:         "build",
			Definition:   def,
			SkippedSteps: []string{"slow-test", "lint"},
		},
		done: make(chan struct{}),
	}

	nodes := map[string]*dagNode{"build": node}

	cfg := runConfig{
		solver: solver,
		sender: sender,
		nodes:  nodes,
		sem:    semaphore.NewWeighted(1),
		tracer: noopTracer(),
	}

	err = runNode(t.Context(), node, cfg)
	require.NoError(t, err)

	// Verify StepSkippedMsg messages.
	var reported []string
	for _, m := range sender.msgs {
		if msg, ok := m.(progressmodel.StepSkippedMsg); ok {
			reported = append(reported, msg.Job+"/"+msg.Step)
		}
	}
	assert.Equal(t, []string{"build/slow-test", "build/lint"}, reported)
}

func TestRunNodeOutputExtractionFailure(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(t.Context())
	require.NoError(t, err)

	outputDef, err := llb.Scratch().Marshal(t.Context())
	require.NoError(t, err)

	extractionErr := errors.New("daemon unavailable")
	solver := &fakeSolver{
		solveFn: func(_ context.Context, d *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			// The output extraction solve uses Exports (ExporterLocal);
			// the job solve does not.
			if len(opt.Exports) > 0 {
				return nil, extractionErr
			}
			return &client.SolveResponse{}, nil
		},
	}

	sender := &fakeSender{}

	node := &dagNode{
		job: rm.Job{
			Name:       "build",
			Definition: def,
			OutputDef:  outputDef,
		},
		done: make(chan struct{}),
	}

	nodes := map[string]*dagNode{"build": node}

	cfg := runConfig{
		solver: solver,
		sender: sender,
		nodes:  nodes,
		sem:    semaphore.NewWeighted(1),
		tracer: noopTracer(),
	}

	err = runNode(t.Context(), node, cfg)
	require.Error(t, err)
	require.ErrorIs(t, err, extractionErr)
	require.Contains(t, err.Error(), "output extraction")
	require.Error(t, node.err)
}

func TestRunNode_jobTimeout(t *testing.T) {
	t.Parallel()

	t.Run("timeout fires on slow solver", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			def, err := llb.Scratch().Marshal(t.Context())
			require.NoError(t, err)

			solver := &fakeSolver{solveFn: func(ctx context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				defer close(ch)
				time.Sleep(10 * time.Second)
				return nil, ctx.Err()
			}}

			err = Run(t.Context(), rm.RunInput{
				Solver: solver,
				Jobs: []rm.Job{{
					Name:       "slow",
					Definition: def,
					Timeout:    1 * time.Second,
				}},
				Sender: &fakeSender{},
			})
			require.Error(t, err)
			assert.ErrorIs(t, err, rm.ErrJobTimeout)
		})
	})

	t.Run("no timeout when job completes quickly", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			def, err := llb.Scratch().Marshal(t.Context())
			require.NoError(t, err)

			solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				close(ch)
				return &client.SolveResponse{}, nil
			}}

			err = Run(t.Context(), rm.RunInput{
				Solver: solver,
				Jobs: []rm.Job{{
					Name:       "fast",
					Definition: def,
					Timeout:    5 * time.Second,
				}},
				Sender: &fakeSender{},
			})
			require.NoError(t, err)
		})
	})
}

func TestRunNode_retry(t *testing.T) {
	t.Parallel()

	t.Run("succeeds on third attempt", func(t *testing.T) {
		t.Parallel()

		def, err := llb.Scratch().Marshal(t.Context())
		require.NoError(t, err)

		var attempts atomic.Int64
		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			defer close(ch)
			n := attempts.Add(1)
			if n < 3 {
				return nil, errors.New("transient failure")
			}
			return &client.SolveResponse{}, nil
		}}

		err = Run(t.Context(), rm.RunInput{
			Solver: solver,
			Jobs: []rm.Job{{
				Name:       "flaky",
				Definition: def,
				Retry:      &pipelinemodel.Retry{Attempts: 3, Backoff: pipelinemodel.BackoffNone},
			}},
			Sender: &fakeSender{},
		})
		require.NoError(t, err)
		assert.Equal(t, int64(3), attempts.Load())
	})

	t.Run("exhausted retries return last error", func(t *testing.T) {
		t.Parallel()

		def, err := llb.Scratch().Marshal(t.Context())
		require.NoError(t, err)

		solveErr := errors.New("permanent failure")
		var attempts atomic.Int64
		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			defer close(ch)
			attempts.Add(1)
			return nil, solveErr
		}}

		err = Run(t.Context(), rm.RunInput{
			Solver: solver,
			Jobs: []rm.Job{{
				Name:       "always-fails",
				Definition: def,
				Retry:      &pipelinemodel.Retry{Attempts: 2, Backoff: pipelinemodel.BackoffNone},
			}},
			Sender: &fakeSender{},
		})
		require.ErrorIs(t, err, solveErr)
		assert.Equal(t, int64(3), attempts.Load(), "1 initial + 2 retries = 3 attempts")
	})

	t.Run("retry with delay and backoff", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			def, err := llb.Scratch().Marshal(t.Context())
			require.NoError(t, err)

			var attempts atomic.Int64
			solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				defer close(ch)
				n := attempts.Add(1)
				if n < 3 {
					return nil, errors.New("transient")
				}
				return &client.SolveResponse{}, nil
			}}

			err = Run(t.Context(), rm.RunInput{
				Solver: solver,
				Jobs: []rm.Job{{
					Name:       "backoff",
					Definition: def,
					Retry: &pipelinemodel.Retry{
						Attempts: 3,
						Delay:    100 * time.Millisecond,
						Backoff:  pipelinemodel.BackoffExponential,
					},
				}},
				Sender: &fakeSender{},
			})
			require.NoError(t, err)
			assert.Equal(t, int64(3), attempts.Load())
		})
	})

	t.Run("timeout cancels retry", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			def, err := llb.Scratch().Marshal(t.Context())
			require.NoError(t, err)

			solver := &fakeSolver{solveFn: func(ctx context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				defer close(ch)
				return nil, errors.New("always fails")
			}}

			err = Run(t.Context(), rm.RunInput{
				Solver: solver,
				Jobs: []rm.Job{{
					Name:       "timeout-retry",
					Definition: def,
					Timeout:    500 * time.Millisecond,
					Retry: &pipelinemodel.Retry{
						Attempts: 100,
						Delay:    200 * time.Millisecond,
						Backoff:  pipelinemodel.BackoffNone,
					},
				}},
				Sender: &fakeSender{},
			})
			require.Error(t, err)
			assert.ErrorIs(t, err, rm.ErrJobTimeout)
		})
	})
}

func TestRetryDelay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		retry   *pipelinemodel.Retry
		attempt int
		want    time.Duration
	}{
		{
			name:    "none backoff returns base delay",
			retry:   &pipelinemodel.Retry{Delay: time.Second, Backoff: pipelinemodel.BackoffNone},
			attempt: 3,
			want:    time.Second,
		},
		{
			name:    "linear backoff",
			retry:   &pipelinemodel.Retry{Delay: time.Second, Backoff: pipelinemodel.BackoffLinear},
			attempt: 2,
			want:    3 * time.Second,
		},
		{
			name:    "exponential backoff attempt 0",
			retry:   &pipelinemodel.Retry{Delay: time.Second, Backoff: pipelinemodel.BackoffExponential},
			attempt: 0,
			want:    time.Second,
		},
		{
			name:    "exponential backoff attempt 3",
			retry:   &pipelinemodel.Retry{Delay: time.Second, Backoff: pipelinemodel.BackoffExponential},
			attempt: 3,
			want:    8 * time.Second,
		},
		{
			name:    "exponential overflow capped at attempt 63",
			retry:   &pipelinemodel.Retry{Delay: time.Hour, Backoff: pipelinemodel.BackoffExponential},
			attempt: 63,
			want:    _maxDelay,
		},
		{
			name:    "linear overflow capped",
			retry:   &pipelinemodel.Retry{Delay: time.Duration(math.MaxInt64 / 2), Backoff: pipelinemodel.BackoffLinear},
			attempt: 2,
			want:    _maxDelay,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := retryDelay(tt.retry, tt.attempt)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestJobTimeoutError(t *testing.T) {
	t.Parallel()

	jte := &rm.JobTimeoutError{JobName: "deploy", Timeout: 30 * time.Second}

	t.Run("errors.Is matches ErrJobTimeout", func(t *testing.T) {
		t.Parallel()
		assert.ErrorIs(t, jte, rm.ErrJobTimeout)
	})

	t.Run("errors.As extracts fields", func(t *testing.T) {
		t.Parallel()
		var target *rm.JobTimeoutError
		require.ErrorAs(t, jte, &target)
		assert.Equal(t, "deploy", target.JobName)
		assert.Equal(t, 30*time.Second, target.Timeout)
	})

	t.Run("Error message format", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, `job "deploy" exceeded 30s timeout`, jte.Error())
	})
}

func TestStepTimeoutError(t *testing.T) {
	t.Parallel()

	ste := &rm.StepTimeoutError{JobName: "build", StepName: "compile", Timeout: 5 * time.Minute}

	t.Run("errors.Is matches ErrStepTimeout", func(t *testing.T) {
		t.Parallel()
		assert.ErrorIs(t, ste, rm.ErrStepTimeout)
	})

	t.Run("errors.As extracts fields", func(t *testing.T) {
		t.Parallel()
		var target *rm.StepTimeoutError
		require.ErrorAs(t, ste, &target)
		assert.Equal(t, "build", target.JobName)
		assert.Equal(t, "compile", target.StepName)
		assert.Equal(t, 5*time.Minute, target.Timeout)
	})

	t.Run("Error message format", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, `step "compile" in job "build" exceeded 5m0s timeout`, ste.Error())
	})
}

func TestRunNode_jobTimeout_sentinelError(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		def, err := llb.Scratch().Marshal(t.Context())
		require.NoError(t, err)

		solver := &fakeSolver{solveFn: func(ctx context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			defer close(ch)
			time.Sleep(10 * time.Second)
			return nil, ctx.Err()
		}}

		node := &dagNode{
			job: rm.Job{
				Name:       "slow-job",
				Definition: def,
				Timeout:    1 * time.Second,
			},
			done: make(chan struct{}),
		}

		nodes := map[string]*dagNode{"slow-job": node}

		cfg := runConfig{
			solver: solver,
			sender: &fakeSender{},
			nodes:  nodes,
			sem:    semaphore.NewWeighted(1),
			tracer: noopTracer(),
		}

		err = runNode(t.Context(), node, cfg)
		require.Error(t, err)
		require.ErrorIs(t, err, rm.ErrJobTimeout)

		var jte *rm.JobTimeoutError
		require.ErrorAs(t, err, &jte)
		assert.Equal(t, "slow-job", jte.JobName)
		assert.Equal(t, 1*time.Second, jte.Timeout)
	})
}

func TestRunNode_retry_displaysRetry(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(t.Context())
	require.NoError(t, err)

	var solveAttempts atomic.Int64
	solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
		defer close(ch)
		n := solveAttempts.Add(1)
		if n < 3 {
			return nil, errors.New("transient")
		}
		return &client.SolveResponse{}, nil
	}}

	sender := &fakeSender{}

	node := &dagNode{
		job: rm.Job{
			Name:       "flaky",
			Definition: def,
			Retry:      &pipelinemodel.Retry{Attempts: 3, Backoff: pipelinemodel.BackoffNone},
		},
		done: make(chan struct{}),
	}

	nodes := map[string]*dagNode{"flaky": node}

	cfg := runConfig{
		solver: solver,
		sender: sender,
		nodes:  nodes,
		sem:    semaphore.NewWeighted(1),
		tracer: noopTracer(),
	}

	err = runNode(t.Context(), node, cfg)
	require.NoError(t, err)
	assert.Equal(t, int64(3), solveAttempts.Load())

	// Extract JobRetryMsg messages.
	type retryCall struct {
		jobName     string
		attempt     int
		maxAttempts int
	}
	var retryCalls []retryCall
	for _, m := range sender.msgs {
		if msg, ok := m.(progressmodel.JobRetryMsg); ok {
			retryCalls = append(retryCalls, retryCall{msg.Job, msg.Attempt, msg.MaxAttempts})
		}
	}
	require.Len(t, retryCalls, 2, "JobRetryMsg sent before each retry attempt")
	// First retry: attempt 2 of 4 (1 initial + 3 retries).
	assert.Equal(t, "flaky", retryCalls[0].jobName)
	assert.Equal(t, 2, retryCalls[0].attempt)
	assert.Equal(t, 4, retryCalls[0].maxAttempts)
	// Second retry: attempt 3 of 4.
	assert.Equal(t, "flaky", retryCalls[1].jobName)
	assert.Equal(t, 3, retryCalls[1].attempt)
	assert.Equal(t, 4, retryCalls[1].maxAttempts)
}

func TestRunNode_jobTimeout_displaysTimeout(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		def, err := llb.Scratch().Marshal(t.Context())
		require.NoError(t, err)

		solver := &fakeSolver{solveFn: func(ctx context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			defer close(ch)
			time.Sleep(10 * time.Second)
			return nil, ctx.Err()
		}}

		sender := &fakeSender{}

		node := &dagNode{
			job: rm.Job{
				Name:       "slow-job",
				Definition: def,
				Timeout:    1 * time.Second,
			},
			done: make(chan struct{}),
		}

		nodes := map[string]*dagNode{"slow-job": node}

		cfg := runConfig{
			solver: solver,
			sender: sender,
			nodes:  nodes,
			sem:    semaphore.NewWeighted(1),
			tracer: noopTracer(),
		}

		err = runNode(t.Context(), node, cfg)
		require.Error(t, err)

		// Extract JobTimeoutMsg messages.
		type timeoutCall struct {
			jobName string
			timeout time.Duration
		}
		var timeoutCalls []timeoutCall
		for _, m := range sender.msgs {
			if msg, ok := m.(progressmodel.JobTimeoutMsg); ok {
				timeoutCalls = append(timeoutCalls, timeoutCall{msg.Job, msg.Timeout})
			}
		}
		require.Len(t, timeoutCalls, 1)
		assert.Equal(t, "slow-job", timeoutCalls[0].jobName)
		assert.Equal(t, 1*time.Second, timeoutCalls[0].timeout)
	})
}

func TestCleanTimeoutError(t *testing.T) {
	t.Parallel()

	jobName := "build"
	stepTimeouts := map[string]time.Duration{
		"build/compile/go build": 30 * time.Second,
	}

	tests := []struct {
		name        string
		err         error
		wantErr     error
		wantExitErr bool // original ExitError should be chained
		wantStepErr bool // should produce *rm.StepTimeoutError
	}{
		{
			name:        "exit code 124 detected as timeout",
			err:         &gatewaypb.ExitError{ExitCode: 124, Err: errors.New("process failed")},
			wantErr:     rm.ErrStepTimeout,
			wantExitErr: true,
			wantStepErr: true,
		},
		{
			name:        "wrapped ExitError 124 found through chain",
			err:         fmt.Errorf("solving job: %w", &gatewaypb.ExitError{ExitCode: 124, Err: errors.New("process failed")}),
			wantErr:     rm.ErrStepTimeout,
			wantExitErr: true,
			wantStepErr: true,
		},
		{
			name:        "exit code 137 detected as timeout (BusyBox timeout -s KILL)",
			err:         &gatewaypb.ExitError{ExitCode: 137, Err: errors.New("process failed")},
			wantErr:     rm.ErrStepTimeout,
			wantExitErr: true,
			wantStepErr: true,
		},
		{
			name: "non-timeout exit code passes through",
			err:  &gatewaypb.ExitError{ExitCode: 1, Err: errors.New("process failed")},
		},
		{
			name: "non-ExitError passes through",
			err:  errors.New("connection refused"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := cleanTimeoutError(tt.err, jobName, stepTimeouts)

			if tt.wantErr != nil {
				require.ErrorIs(t, got, tt.wantErr)
			}
			if tt.wantExitErr {
				var exitErr *gatewaypb.ExitError
				require.ErrorAs(t, got, &exitErr)
			}
			if tt.wantStepErr {
				var ste *rm.StepTimeoutError
				require.ErrorAs(t, got, &ste)
				assert.Equal(t, jobName, ste.JobName)
				assert.Equal(t, "compile", ste.StepName)
				assert.Equal(t, 30*time.Second, ste.Timeout)
			}
		})
	}
}

func TestCleanTimeoutError_multipleStepTimeouts(t *testing.T) {
	t.Parallel()

	jobName := "build"
	stepTimeouts := map[string]time.Duration{
		"build/compile/go build": 30 * time.Second,
		"build/test/go test":     60 * time.Second,
	}

	exitErr := &gatewaypb.ExitError{ExitCode: 124, Err: errors.New("process failed")}
	got := cleanTimeoutError(exitErr, jobName, stepTimeouts)

	// rm.StepTimeoutError is still produced (signals rm.ErrStepTimeout).
	require.ErrorIs(t, got, rm.ErrStepTimeout)
	var ste *rm.StepTimeoutError
	require.ErrorAs(t, got, &ste)
	assert.Equal(t, jobName, ste.JobName)
	// With multiple entries, step attribution is ambiguous — fields are empty.
	assert.Empty(t, ste.StepName)
	assert.Zero(t, ste.Timeout)
}

func TestRun_failFast(t *testing.T) {
	t.Parallel()

	t.Run("fail-fast cancels independent jobs", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			defA, err := llb.Scratch().Marshal(t.Context())
			require.NoError(t, err)
			defB, err := llb.Scratch().Marshal(t.Context())
			require.NoError(t, err)
			defC, err := llb.Scratch().Marshal(t.Context())
			require.NoError(t, err)

			solver := &fakeSolver{solveFn: func(ctx context.Context, d *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				defer close(ch)
				switch d {
				case defA:
					return nil, errors.New("job a exploded")
				case defC:
					<-ctx.Done()
					return nil, ctx.Err()
				default:
					return &client.SolveResponse{}, nil
				}
			}}

			err = Run(t.Context(), rm.RunInput{
				Solver: solver,
				Jobs: []rm.Job{
					{Name: "a", Definition: defA},
					{Name: "b", Definition: defB},
					{Name: "c", Definition: defC},
				},
				Sender:   &fakeSender{},
				FailFast: true,
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "job a exploded")
		})
	})

	t.Run("no-fail-fast lets independent jobs complete", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			defA, err := llb.Scratch().Marshal(t.Context())
			require.NoError(t, err)
			defB, err := llb.Scratch().Marshal(t.Context())
			require.NoError(t, err)
			defC, err := llb.Scratch().Marshal(t.Context())
			require.NoError(t, err)

			var completed sync.Map

			solver := &fakeSolver{solveFn: func(_ context.Context, d *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				defer close(ch)
				switch d {
				case defA:
					return nil, errors.New("job a exploded")
				default:
					time.Sleep(10 * time.Millisecond)
					completed.Store(d, true)
					return &client.SolveResponse{}, nil
				}
			}}

			err = Run(t.Context(), rm.RunInput{
				Solver: solver,
				Jobs: []rm.Job{
					{Name: "a", Definition: defA},
					{Name: "b", Definition: defB},
					{Name: "c", Definition: defC},
				},
				Sender:   &fakeSender{},
				FailFast: false,
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "job a exploded")

			_, bDone := completed.Load(defB)
			_, cDone := completed.Load(defC)
			assert.True(t, bDone, "job b should complete despite a's failure")
			assert.True(t, cDone, "job c should complete despite a's failure")
		})
	})

	t.Run("no-fail-fast propagates dep errors", func(t *testing.T) {
		t.Parallel()

		defA, err := llb.Scratch().Marshal(t.Context())
		require.NoError(t, err)
		defB, err := llb.Scratch().Marshal(t.Context())
		require.NoError(t, err)
		defD, err := llb.Scratch().Marshal(t.Context())
		require.NoError(t, err)

		var dCompleted atomic.Bool

		solver := &fakeSolver{solveFn: func(_ context.Context, d *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			defer close(ch)
			switch d {
			case defA:
				return nil, errors.New("job a exploded")
			case defD:
				dCompleted.Store(true)
				return &client.SolveResponse{}, nil
			default:
				return &client.SolveResponse{}, nil
			}
		}}

		sender := &fakeSender{}

		err = Run(t.Context(), rm.RunInput{
			Solver: solver,
			Jobs: []rm.Job{
				{Name: "a", Definition: defA},
				{Name: "b", Definition: defB, DependsOn: []string{"a"}},
				{Name: "d", Definition: defD},
			},
			Sender:   sender,
			FailFast: false,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "job a exploded")
		assert.Contains(t, err.Error(), `dependency "a"`)
		assert.True(t, dCompleted.Load(), "independent job d should complete")
		assert.False(t, sender.hasJobAdded("b"), "job b should not be solved when dep a fails")
	})

	t.Run("no-fail-fast filters exports", func(t *testing.T) {
		t.Parallel()

		defA, err := llb.Scratch().Marshal(t.Context())
		require.NoError(t, err)
		defB, err := llb.Scratch().Marshal(t.Context())
		require.NoError(t, err)
		exportDefA, err := llb.Scratch().Marshal(t.Context())
		require.NoError(t, err)
		exportDefB, err := llb.Scratch().Marshal(t.Context())
		require.NoError(t, err)

		var exportedJobs sync.Map

		solver := &fakeSolver{solveFn: func(_ context.Context, d *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			defer close(ch)
			if len(opt.Exports) > 0 {
				if d == exportDefA {
					exportedJobs.Store("a", true)
				}
				if d == exportDefB {
					exportedJobs.Store("b", true)
				}
				return &client.SolveResponse{}, nil
			}
			if d == defA {
				return nil, errors.New("job a exploded")
			}
			return &client.SolveResponse{}, nil
		}}

		err = Run(t.Context(), rm.RunInput{
			Solver: solver,
			Jobs: []rm.Job{
				{Name: "a", Definition: defA},
				{Name: "b", Definition: defB},
			},
			Sender:   &fakeSender{},
			FailFast: false,
			Exports: []rm.Export{
				{Definition: exportDefA, JobName: "a", Local: "/tmp/a/out"},
				{Definition: exportDefB, JobName: "b", Local: "/tmp/b/out"},
			},
		})
		require.Error(t, err)

		_, aExported := exportedJobs.Load("a")
		_, bExported := exportedJobs.Load("b")
		assert.False(t, aExported, "failed job a's export should be filtered out")
		assert.True(t, bExported, "successful job b's export should run")
	})

	t.Run("no-fail-fast joins multiple errors", func(t *testing.T) {
		t.Parallel()

		defA, err := llb.Scratch().Marshal(t.Context())
		require.NoError(t, err)
		defB, err := llb.Scratch().Marshal(t.Context())
		require.NoError(t, err)

		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			defer close(ch)
			return nil, errors.New("boom")
		}}

		err = Run(t.Context(), rm.RunInput{
			Solver: solver,
			Jobs: []rm.Job{
				{Name: "a", Definition: defA},
				{Name: "b", Definition: defB},
			},
			Sender:   &fakeSender{},
			FailFast: false,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `job "a"`)
		assert.Contains(t, err.Error(), `job "b"`)
	})
}

func TestStepControlledExecution(t *testing.T) {
	t.Parallel()

	// stepBuildFunc returns a simple rm.StepExec.Build that produces a scratch state.
	stepBuildFunc := func(base llb.State, _ int) (llb.State, error) {
		return llb.Scratch(), nil
	}

	t.Run("all steps succeed", func(t *testing.T) {
		t.Parallel()
		sender := &fakeSender{}
		var solveCount atomic.Int32

		gc := &fakeGatewayClient{solveFn: func(_ context.Context, _ gateway.SolveRequest) (*gateway.Result, error) {
			solveCount.Add(1)
			return gateway.NewResult(), nil
		}}

		solver := &fakeSolver{
			buildFn: func(ctx context.Context, _ client.SolveOpt, _ string, buildFunc gateway.BuildFunc, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				defer close(ch)
				_, err := buildFunc(ctx, gc)
				return &client.SolveResponse{}, err
			},
		}

		def, err := llb.Scratch().Marshal(t.Context())
		require.NoError(t, err)

		err = Run(t.Context(), rm.RunInput{
			Solver: solver,
			Jobs: []rm.Job{{
				Name:       "test",
				Definition: def,
				Steps: []rm.StepExec{
					{Name: "a", Build: stepBuildFunc},
					{Name: "b", Build: stepBuildFunc},
				},
				BaseState: llb.Scratch(),
			}},
			Sender: sender,
		})
		require.NoError(t, err)
		// 2 steps solved.
		assert.Equal(t, int32(2), solveCount.Load())
	})

	t.Run("step retry succeeds after failures", func(t *testing.T) {
		t.Parallel()
		sender := &fakeSender{}
		var solveCount atomic.Int32

		gc := &fakeGatewayClient{solveFn: func(_ context.Context, _ gateway.SolveRequest) (*gateway.Result, error) {
			n := solveCount.Add(1)
			if n <= 2 {
				return nil, errors.New("transient")
			}
			return gateway.NewResult(), nil
		}}

		solver := &fakeSolver{
			buildFn: func(ctx context.Context, _ client.SolveOpt, _ string, buildFunc gateway.BuildFunc, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				defer close(ch)
				_, err := buildFunc(ctx, gc)
				return &client.SolveResponse{}, err
			},
		}

		def, err := llb.Scratch().Marshal(t.Context())
		require.NoError(t, err)

		err = Run(t.Context(), rm.RunInput{
			Solver: solver,
			Jobs: []rm.Job{{
				Name:       "test",
				Definition: def,
				Steps: []rm.StepExec{
					{
						Name:  "flaky",
						Retry: &pipelinemodel.Retry{Attempts: 3, Backoff: pipelinemodel.BackoffNone},
						Build: stepBuildFunc,
					},
				},
				BaseState: llb.Scratch(),
			}},
			Sender: sender,
		})
		require.NoError(t, err)
		// 2 failures + 1 success = 3 solves.
		assert.Equal(t, int32(3), solveCount.Load())

		// Verify StepRetryMsg was sent.
		sender.mu.Lock()
		var retryMsgs []progressmodel.StepRetryMsg
		for _, m := range sender.msgs {
			if msg, ok := m.(progressmodel.StepRetryMsg); ok {
				retryMsgs = append(retryMsgs, msg)
			}
		}
		sender.mu.Unlock()
		assert.Len(t, retryMsgs, 2)
	})

	t.Run("allow-failure continues to next step", func(t *testing.T) {
		t.Parallel()
		sender := &fakeSender{}
		var solveCount atomic.Int32

		gc := &fakeGatewayClient{solveFn: func(_ context.Context, _ gateway.SolveRequest) (*gateway.Result, error) {
			n := solveCount.Add(1)
			if n == 1 {
				return nil, errors.New("allowed failure")
			}
			return gateway.NewResult(), nil
		}}

		solver := &fakeSolver{
			buildFn: func(ctx context.Context, _ client.SolveOpt, _ string, buildFunc gateway.BuildFunc, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				defer close(ch)
				_, err := buildFunc(ctx, gc)
				return &client.SolveResponse{}, err
			},
		}

		def, err := llb.Scratch().Marshal(t.Context())
		require.NoError(t, err)

		err = Run(t.Context(), rm.RunInput{
			Solver: solver,
			Jobs: []rm.Job{{
				Name:       "test",
				Definition: def,
				Steps: []rm.StepExec{
					{Name: "optional", AllowFailure: true, Build: stepBuildFunc},
					{Name: "required", Build: stepBuildFunc},
				},
				BaseState: llb.Scratch(),
			}},
			Sender: sender,
		})
		require.NoError(t, err)
		// 1 failure (allowed) + 1 success = 2 solves.
		assert.Equal(t, int32(2), solveCount.Load())

		// Verify StepAllowedFailureMsg was sent.
		sender.mu.Lock()
		var allowedMsgs []progressmodel.StepAllowedFailureMsg
		for _, m := range sender.msgs {
			if msg, ok := m.(progressmodel.StepAllowedFailureMsg); ok {
				allowedMsgs = append(allowedMsgs, msg)
			}
		}
		sender.mu.Unlock()
		require.Len(t, allowedMsgs, 1)
		assert.Equal(t, "optional", allowedMsgs[0].Step)
	})

	t.Run("step failure without allow-failure fails job", func(t *testing.T) {
		t.Parallel()
		sender := &fakeSender{}

		var solveCount atomic.Int32
		gc := &fakeGatewayClient{solveFn: func(_ context.Context, _ gateway.SolveRequest) (*gateway.Result, error) {
			solveCount.Add(1)
			return nil, errors.New("boom")
		}}

		solver := &fakeSolver{
			buildFn: func(ctx context.Context, _ client.SolveOpt, _ string, buildFunc gateway.BuildFunc, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				defer close(ch)
				_, err := buildFunc(ctx, gc)
				return &client.SolveResponse{}, err
			},
		}

		def, err := llb.Scratch().Marshal(t.Context())
		require.NoError(t, err)

		err = Run(t.Context(), rm.RunInput{
			Solver: solver,
			Jobs: []rm.Job{{
				Name:       "test",
				Definition: def,
				Steps: []rm.StepExec{
					{Name: "fail", Build: stepBuildFunc},
					{Name: "never-reached", Build: stepBuildFunc},
				},
				BaseState: llb.Scratch(),
			}},
			Sender: sender,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
		assert.Equal(t, int32(1), solveCount.Load(), "only the first step should be solved")
	})

	t.Run("retry then allow-failure", func(t *testing.T) {
		t.Parallel()
		sender := &fakeSender{}
		var solveCount atomic.Int32

		gc := &fakeGatewayClient{solveFn: func(_ context.Context, _ gateway.SolveRequest) (*gateway.Result, error) {
			solveCount.Add(1)
			return nil, errors.New("always fails")
		}}

		solver := &fakeSolver{
			buildFn: func(ctx context.Context, _ client.SolveOpt, _ string, buildFunc gateway.BuildFunc, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				defer close(ch)
				_, err := buildFunc(ctx, gc)
				return &client.SolveResponse{}, err
			},
		}

		def, err := llb.Scratch().Marshal(t.Context())
		require.NoError(t, err)

		err = Run(t.Context(), rm.RunInput{
			Solver: solver,
			Jobs: []rm.Job{{
				Name:       "test",
				Definition: def,
				Steps: []rm.StepExec{
					{
						Name:         "flaky",
						Retry:        &pipelinemodel.Retry{Attempts: 2, Backoff: pipelinemodel.BackoffNone},
						AllowFailure: true,
						Build:        stepBuildFunc,
					},
				},
				BaseState: llb.Scratch(),
			}},
			Sender: sender,
		})
		require.NoError(t, err)
		// 1 initial + 2 retries = 3 solves, then allowed to fail.
		assert.Equal(t, int32(3), solveCount.Load())

		sender.mu.Lock()
		var retryCount, allowedCount int
		for _, m := range sender.msgs {
			switch m.(type) {
			case progressmodel.StepRetryMsg:
				retryCount++
			case progressmodel.StepAllowedFailureMsg:
				allowedCount++
			default:
			}
		}
		sender.mu.Unlock()
		assert.Equal(t, 2, retryCount)
		assert.Equal(t, 1, allowedCount)
	})
}
