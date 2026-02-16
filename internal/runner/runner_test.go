package runner

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"golang.org/x/sync/semaphore"

	"github.com/ndisidore/cicada/internal/cache"
	"github.com/ndisidore/cicada/pkg/conditional"
	"github.com/ndisidore/cicada/pkg/pipeline"
)

// fakeSolver implements the Solver interface for testing.
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

// MockCondition is a testify mock for the DeferredEvaluator interface.
type MockCondition struct {
	mock.Mock
}

func (m *MockCondition) EvaluateDeferred(ctx conditional.Context, depOutputs map[string]map[string]string) (bool, error) {
	args := m.Called(ctx, depOutputs)
	return args.Bool(0), args.Error(1)
}

// fakeDisplay implements progress.Display for testing.
type fakeDisplay struct {
	wg         sync.WaitGroup
	attachFn   func(ctx context.Context, name string, ch <-chan *client.SolveStatus) error
	skipFn     func(ctx context.Context, jobName string)
	skipStepFn func(ctx context.Context, jobName, stepName string)
}

func (*fakeDisplay) Start(_ context.Context) error { return nil }

func (f *fakeDisplay) Attach(ctx context.Context, name string, ch <-chan *client.SolveStatus) error {
	if f.attachFn != nil {
		return f.attachFn(ctx, name, ch)
	}
	f.wg.Go(func() {
		//revive:disable-next-line:empty-block // drain
		for range ch {
		}
	})
	return nil
}

func (f *fakeDisplay) Skip(ctx context.Context, jobName string) {
	if f.skipFn != nil {
		f.skipFn(ctx, jobName)
	}
}

func (f *fakeDisplay) SkipStep(ctx context.Context, jobName, stepName string) {
	if f.skipStepFn != nil {
		f.skipStepFn(ctx, jobName, stepName)
	}
}

func (*fakeDisplay) Seal() {}

func (f *fakeDisplay) Wait() error { f.wg.Wait(); return nil }

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
	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name         string
		ctx          context.Context // if nil, context.Background() is used
		input        RunInput
		wantErr      string
		wantSentinel error
	}{
		{
			name: "single job solves successfully",
			input: RunInput{
				Solver: &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
					close(ch)
					return &client.SolveResponse{}, nil
				}},
				Jobs:    []Job{{Name: "build", Definition: def}},
				Display: &fakeDisplay{},
			},
		},
		{
			name: "multi-job execution",
			input: RunInput{
				Solver: &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
					close(ch)
					return &client.SolveResponse{}, nil
				}},
				Jobs: []Job{
					{Name: "first", Definition: def},
					{Name: "second", Definition: def},
					{Name: "third", Definition: def},
				},
				Display: &fakeDisplay{},
			},
		},
		{
			name: "solve error wraps with job name",
			input: RunInput{
				Solver: &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
					close(ch)
					return nil, errors.New("connection refused")
				}},
				Jobs:    []Job{{Name: "deploy", Definition: def}},
				Display: &fakeDisplay{},
			},
			wantErr: `job "deploy"`,
		},
		{
			name: "empty jobs is a no-op",
			input: RunInput{
				Solver:  &fakeSolver{},
				Jobs:    []Job{},
				Display: &fakeDisplay{},
			},
		},
		{
			name: "nil solver returns error",
			input: RunInput{
				Solver:  nil,
				Jobs:    []Job{{Name: "x", Definition: def}},
				Display: &fakeDisplay{},
			},
			wantSentinel: ErrNilSolver,
		},
		{
			name: "nil display returns error",
			input: RunInput{
				Solver:  &fakeSolver{},
				Jobs:    []Job{{Name: "x", Definition: def}},
				Display: nil,
			},
			wantSentinel: ErrNilDisplay,
		},
		{
			name: "unknown dependency",
			input: RunInput{
				Solver:  &fakeSolver{},
				Jobs:    []Job{{Name: "a", Definition: def, DependsOn: []string{"nonexistent"}}},
				Display: &fakeDisplay{},
			},
			wantSentinel: pipeline.ErrUnknownDep,
		},
		{
			name: "duplicate job name",
			input: RunInput{
				Solver: &fakeSolver{},
				Jobs: []Job{
					{Name: "a", Definition: def},
					{Name: "a", Definition: def},
				},
				Display: &fakeDisplay{},
			},
			wantSentinel: pipeline.ErrDuplicateJob,
		},
		{
			name: "mutual cycle",
			input: RunInput{
				Solver: &fakeSolver{},
				Jobs: []Job{
					{Name: "a", Definition: def, DependsOn: []string{"b"}},
					{Name: "b", Definition: def, DependsOn: []string{"a"}},
				},
				Display: &fakeDisplay{},
			},
			wantSentinel: pipeline.ErrCycleDetected,
		},
		{
			name: "self cycle",
			input: RunInput{
				Solver: &fakeSolver{},
				Jobs: []Job{
					{Name: "a", Definition: def, DependsOn: []string{"a"}},
				},
				Display: &fakeDisplay{},
			},
			wantSentinel: pipeline.ErrCycleDetected,
		},
		{
			name: "nil definition returns error",
			input: RunInput{
				Solver:  &fakeSolver{},
				Jobs:    []Job{{Name: "bad", Definition: nil}},
				Display: &fakeDisplay{},
			},
			wantSentinel: ErrNilDefinition,
		},
		{
			name: "context cancellation propagates",
			ctx:  cancelledCtx,
			input: RunInput{
				Solver: &fakeSolver{solveFn: func(ctx context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
					close(ch)
					return nil, ctx.Err()
				}},
				Jobs:    []Job{{Name: "cancelled", Definition: def}},
				Display: &fakeDisplay{},
			},
			wantSentinel: context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := tt.ctx
			if ctx == nil {
				ctx = context.Background()
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

	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	t.Run("linear chain", func(t *testing.T) {
		t.Parallel()

		var mu sync.Mutex
		var order []string

		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			return &client.SolveResponse{}, nil
		}}
		display := &fakeDisplay{}
		display.attachFn = func(_ context.Context, name string, ch <-chan *client.SolveStatus) error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			display.wg.Go(func() {
				//revive:disable-next-line:empty-block // drain
				for range ch {
				}
			})
			return nil
		}

		// Chain a -> b -> c so they must run in order.
		err := Run(context.Background(), RunInput{
			Solver: solver,
			Jobs: []Job{
				{Name: "a", Definition: def},
				{Name: "b", Definition: def, DependsOn: []string{"a"}},
				{Name: "c", Definition: def, DependsOn: []string{"b"}},
			},
			Display: display,
		})
		require.NoError(t, err)
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

		var mu sync.Mutex
		completed := make(map[string]bool)

		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			return &client.SolveResponse{}, nil
		}}
		display := &fakeDisplay{}
		display.attachFn = func(_ context.Context, name string, ch <-chan *client.SolveStatus) error {
			mu.Lock()
			switch name {
			case "b", "c":
				assert.True(t, completed["a"], "%s should run after a", name)
			case "d":
				assert.True(t, completed["b"], "d should run after b")
				assert.True(t, completed["c"], "d should run after c")
			default:
			}
			completed[name] = true
			mu.Unlock()
			display.wg.Go(func() {
				//revive:disable-next-line:empty-block // drain
				for range ch {
				}
			})
			return nil
		}

		// Diamond: a -> {b, c} -> d
		err := Run(context.Background(), RunInput{
			Solver: solver,
			Jobs: []Job{
				{Name: "a", Definition: def},
				{Name: "b", Definition: def, DependsOn: []string{"a"}},
				{Name: "c", Definition: def, DependsOn: []string{"a"}},
				{Name: "d", Definition: def, DependsOn: []string{"b", "c"}},
			},
			Display: display,
		})
		require.NoError(t, err)
		assert.Len(t, completed, 4)
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

			err = Run(t.Context(), RunInput{
				Solver: solver,
				Jobs: []Job{
					{Name: "a", Definition: def},
					{Name: "b", Definition: def},
					{Name: "c", Definition: def},
				},
				Display: &fakeDisplay{},
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

			err = Run(t.Context(), RunInput{
				Solver: solver,
				Jobs: []Job{
					{Name: "a", Definition: def},
					{Name: "b", Definition: def},
					{Name: "c", Definition: def},
					{Name: "d", Definition: def},
				},
				Display:     &fakeDisplay{},
				Parallelism: 2,
			})
			require.NoError(t, err)
			assert.LessOrEqual(t, maxConcurrent.Load(), int64(2), "should not exceed parallelism=2")
		})
	})
}

func TestRun_errorPropagation(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(context.Background())
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

		var cExecuted atomic.Bool

		display := &fakeDisplay{}
		display.attachFn = func(_ context.Context, name string, ch <-chan *client.SolveStatus) error {
			if name == "c" {
				cExecuted.Store(true)
			}
			display.wg.Go(func() {
				//revive:disable-next-line:empty-block // drain
				for range ch {
				}
			})
			return nil
		}

		// "a" succeeds, "b" fails at solve, "c" depends on "b".
		solver := &fakeSolver{solveFn: func(_ context.Context, def *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			if def == defB {
				return nil, errors.New("job b exploded")
			}
			return &client.SolveResponse{}, nil
		}}

		err = Run(context.Background(), RunInput{
			Solver: solver,
			Jobs: []Job{
				{Name: "a", Definition: defA},
				{Name: "b", Definition: defB, DependsOn: []string{"a"}},
				{Name: "c", Definition: defC, DependsOn: []string{"b"}},
			},
			Display: display,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "job b exploded")
		assert.False(t, cExecuted.Load(), "job c should never execute when dep b fails")
	})

	t.Run("propagates to grandchild", func(t *testing.T) {
		t.Parallel()

		var cSolved atomic.Bool

		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			return nil, errors.New("job a failed")
		}}
		display := &fakeDisplay{}
		display.attachFn = func(_ context.Context, name string, ch <-chan *client.SolveStatus) error {
			if name == "c" {
				cSolved.Store(true)
			}
			display.wg.Go(func() {
				//revive:disable-next-line:empty-block // drain
				for range ch {
				}
			})
			return nil
		}

		// Chain a -> b -> c. Job "a" fails; "c" should never be solved.
		err := Run(context.Background(), RunInput{
			Solver: solver,
			Jobs: []Job{
				{Name: "a", Definition: def},
				{Name: "b", Definition: def, DependsOn: []string{"a"}},
				{Name: "c", Definition: def, DependsOn: []string{"b"}},
			},
			Display:     display,
			Parallelism: 1,
		})
		require.Error(t, err)
		assert.False(t, cSolved.Load(), "job c should never be solved when grandparent a fails")
	})

	t.Run("failed job unblocks deps", func(t *testing.T) {
		t.Parallel()

		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			return nil, errors.New("job a failed")
		}}

		display := &fakeDisplay{}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		err := Run(ctx, RunInput{
			Solver: solver,
			Jobs: []Job{
				{Name: "a", Definition: def},
				{Name: "b", Definition: def, DependsOn: []string{"a"}},
			},
			Display: display,
		})
		require.Error(t, err)
		// Must not be a context deadline exceeded (that would mean it hung).
		assert.NotErrorIs(t, err, context.DeadlineExceeded)
	})

	t.Run("dep error skips solve", func(t *testing.T) {
		t.Parallel()

		var bSolved atomic.Bool

		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			return nil, errors.New("job a failed")
		}}
		display := &fakeDisplay{}
		display.attachFn = func(_ context.Context, name string, ch <-chan *client.SolveStatus) error {
			if name == "b" {
				bSolved.Store(true)
			}
			display.wg.Go(func() {
				//revive:disable-next-line:empty-block // drain
				for range ch {
				}
			})
			return nil
		}

		err := Run(context.Background(), RunInput{
			Solver: solver,
			Jobs: []Job{
				{Name: "a", Definition: def},
				{Name: "b", Definition: def, DependsOn: []string{"a"}},
			},
			Display:     display,
			Parallelism: 1,
		})
		require.Error(t, err)
		assert.False(t, bSolved.Load(), "job b's solve should never be invoked when dep a fails")
	})
}

func TestRun_display(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	t.Run("solver writes status events", func(t *testing.T) {
		t.Parallel()

		var received atomic.Int64
		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			ch <- &client.SolveStatus{}
			ch <- &client.SolveStatus{}
			close(ch)
			return &client.SolveResponse{}, nil
		}}
		display := &fakeDisplay{}
		display.attachFn = func(_ context.Context, _ string, ch <-chan *client.SolveStatus) error {
			display.wg.Go(func() {
				for range ch {
					received.Add(1)
				}
			})
			return nil
		}

		err := Run(context.Background(), RunInput{
			Solver:  solver,
			Jobs:    []Job{{Name: "status-test", Definition: def}},
			Display: display,
		})
		require.NoError(t, err)
		require.NoError(t, display.Wait())
		assert.Equal(t, int64(2), received.Load())
	})
}

func TestRun_semAcquireFailurePropagates(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	var solved atomic.Bool
	solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
		solved.Store(true)
		close(ch)
		return &client.SolveResponse{}, nil
	}}

	// Pre-cancel so sem.Acquire fails immediately for all jobs.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = Run(ctx, RunInput{
		Solver: solver,
		Jobs: []Job{
			{Name: "a", Definition: def},
			{Name: "b", Definition: def, DependsOn: []string{"a"}},
		},
		Display:     &fakeDisplay{},
		Parallelism: 1,
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, solved.Load(), "solver should not be called when sem.Acquire fails")
}

func TestRun_exports(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	tests := []struct {
		name          string
		exports       []Export
		solver        *fakeSolver
		display       *fakeDisplay
		wantErr       string
		wantSentinel  error
		checkOpt      bool   // whether to assert on capturedExportOpt after Run
		wantOutputDir string // expected OutputDir when checkOpt is true
	}{
		{
			name: "export solves with local exporter",
			exports: []Export{
				{Definition: def, JobName: "build", Local: "/tmp/out/myapp"},
			},
			solver: &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				close(ch)
				return &client.SolveResponse{}, nil
			}},
			display:       &fakeDisplay{},
			checkOpt:      true,
			wantOutputDir: "/tmp/out",
		},
		{
			name: "directory export uses Local as OutputDir",
			exports: []Export{
				{Definition: def, JobName: "build", Local: "/tmp/out/dist", Dir: true},
			},
			solver: &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				close(ch)
				return &client.SolveResponse{}, nil
			}},
			display:       &fakeDisplay{},
			checkOpt:      true,
			wantOutputDir: "/tmp/out/dist",
		},
		{
			name: "export solve error wraps job and path",
			exports: []Export{
				{Definition: def, JobName: "compile", Local: "/tmp/bin/app"},
			},
			solver: &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				close(ch)
				return nil, errors.New("disk full")
			}},
			display: &fakeDisplay{},
			wantErr: `exporting "/tmp/bin/app" from job "compile"`,
		},
		{
			name: "nil export definition returns error",
			exports: []Export{
				{Definition: nil, JobName: "bad", Local: "/tmp/out/x"},
			},
			solver:       &fakeSolver{},
			display:      &fakeDisplay{},
			wantSentinel: ErrNilDefinition,
		},
		{
			name: "empty export local returns error",
			exports: []Export{
				{Definition: def, JobName: "bad", Local: ""},
			},
			solver:       &fakeSolver{},
			display:      &fakeDisplay{},
			wantSentinel: pipeline.ErrEmptyExportLocal,
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

			err := Run(context.Background(), RunInput{
				Solver:  jobSolver,
				Jobs:    []Job{{Name: "job", Definition: def}},
				Display: tt.display,
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

			err = Run(t.Context(), RunInput{
				Solver:  solver,
				Jobs:    []Job{{Name: "build", Definition: def}},
				Display: &fakeDisplay{},
				Exports: []Export{
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

			err = Run(t.Context(), RunInput{
				Solver:  solver,
				Jobs:    []Job{{Name: "build", Definition: def}},
				Display: &fakeDisplay{},
				Exports: []Export{
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

	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	exports := []client.CacheOptionsEntry{{Type: "registry", Attrs: map[string]string{"ref": "ghcr.io/org/cache"}}}
	imports := []client.CacheOptionsEntry{{Type: "local", Attrs: map[string]string{"src": "/tmp/cache"}}}

	var capturedOpt atomic.Pointer[client.SolveOpt]
	solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
		capturedOpt.Store(&opt)
		close(ch)
		return &client.SolveResponse{}, nil
	}}

	err = Run(context.Background(), RunInput{
		Solver:       solver,
		Jobs:         []Job{{Name: "build", Definition: def}},
		Display:      &fakeDisplay{},
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

	def, err := llb.Scratch().Marshal(context.Background())
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

		err := Run(context.Background(), RunInput{
			Solver:  solver,
			Jobs:    []Job{{Name: "build", Definition: def}},
			Display: &fakeDisplay{},
			ImagePublishes: []ImagePublish{
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

		err := Run(context.Background(), RunInput{
			Solver:  solver,
			Jobs:    []Job{{Name: "build", Definition: def}},
			Display: &fakeDisplay{},
			ImagePublishes: []ImagePublish{
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

		err := Run(context.Background(), RunInput{
			Solver:  solver,
			Jobs:    []Job{{Name: "build", Definition: def}},
			Display: &fakeDisplay{},
			ImagePublishes: []ImagePublish{
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

		err := Run(context.Background(), RunInput{
			Solver:  solver,
			Jobs:    []Job{{Name: "build", Definition: def}},
			Display: &fakeDisplay{},
			ImagePublishes: []ImagePublish{
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

		err := Run(context.Background(), RunInput{
			Solver:  solver,
			Jobs:    []Job{{Name: "build", Definition: def}},
			Display: &fakeDisplay{},
			ImagePublishes: []ImagePublish{
				{Definition: nil, JobName: "build", Image: "ghcr.io/user/app:v1", Push: true},
			},
		})
		require.ErrorIs(t, err, ErrNilDefinition)
	})
}

func TestGroupPublishes(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	t.Run("single variant produces one group", func(t *testing.T) {
		t.Parallel()

		pubs := []ImagePublish{
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

		pubs := []ImagePublish{
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

		pubs := []ImagePublish{
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

		pubs := []ImagePublish{
			{Definition: def, JobName: "build-amd64", Image: "ghcr.io/user/app:v1", Push: true, Platform: "linux/amd64"},
			{Definition: def, JobName: "build-arm64", Image: "ghcr.io/user/app:v1", Push: false, Platform: "linux/arm64"},
		}
		_, err := groupPublishes(pubs)
		require.ErrorIs(t, err, ErrPublishSettingConflict)
	})

	t.Run("conflicting export-docker setting rejected", func(t *testing.T) {
		t.Parallel()

		pubs := []ImagePublish{
			{Definition: def, JobName: "build-amd64", Image: "app:v1", ExportDocker: true, Platform: "linux/amd64"},
			{Definition: def, JobName: "build-arm64", Image: "app:v1", ExportDocker: false, Platform: "linux/arm64"},
		}
		_, err := groupPublishes(pubs)
		require.ErrorIs(t, err, ErrPublishSettingConflict)
	})
}

func TestGroupPublishes_exportDocker(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	pubs := []ImagePublish{
		{Definition: def, JobName: "build", Image: "myapp:dev", ExportDocker: true, Platform: "linux/amd64"},
	}
	groups, err := groupPublishes(pubs)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	assert.True(t, groups[0].ExportDocker)
}

func TestRun_exportDockerMultiPlatformError(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
		close(ch)
		return &client.SolveResponse{}, nil
	}}

	err = Run(context.Background(), RunInput{
		Solver:  solver,
		Jobs:    []Job{{Name: "build", Definition: def}},
		Display: &fakeDisplay{},
		ImagePublishes: []ImagePublish{
			{Definition: def, JobName: "build-amd64", Image: "myapp:latest", ExportDocker: true, Platform: "linux/amd64"},
			{Definition: def, JobName: "build-arm64", Image: "myapp:latest", ExportDocker: true, Platform: "linux/arm64"},
		},
	})
	require.ErrorIs(t, err, ErrExportDockerMultiPlatform)
}

func TestRun_duplicatePlatformError(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(context.Background())
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

	err = Run(context.Background(), RunInput{
		Solver:  solver,
		Jobs:    []Job{{Name: "build", Definition: def}},
		Display: &fakeDisplay{},
		ImagePublishes: []ImagePublish{
			{Definition: def, JobName: "build-a", Image: "myapp:latest", Push: true, Platform: "linux/amd64"},
			{Definition: def, JobName: "build-b", Image: "myapp:latest", Push: true, Platform: "linux/amd64"},
		},
	})
	require.ErrorIs(t, err, ErrDuplicatePlatform)
}

// TestRun_exportDocker tests the export-docker path. Subtests are sequential
// because they override the package-level _dockerLoadCmd variable.
func TestRun_exportDocker(t *testing.T) {
	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	origCmd := _dockerLoadCmd
	t.Cleanup(func() { _dockerLoadCmd = origCmd })

	_dockerLoadCmd = func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "cat")
	}

	t.Run("solves with docker exporter and Output set", func(t *testing.T) {
		var capturedOpt atomic.Pointer[client.SolveOpt]
		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			if len(opt.Exports) > 0 && opt.Exports[0].Type == client.ExporterDocker {
				capturedOpt.Store(&opt)
			}
			close(ch)
			return &client.SolveResponse{}, nil
		}}

		err := Run(context.Background(), RunInput{
			Solver:  solver,
			Jobs:    []Job{{Name: "build", Definition: def}},
			Display: &fakeDisplay{},
			ImagePublishes: []ImagePublish{
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
	})

	t.Run("push and export-docker run concurrently", func(t *testing.T) {
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

		err := Run(context.Background(), RunInput{
			Solver:  solver,
			Jobs:    []Job{{Name: "build", Definition: def}},
			Display: &fakeDisplay{},
			ImagePublishes: []ImagePublish{
				{Definition: def, JobName: "build", Image: "ghcr.io/user/app:v1", Push: true, ExportDocker: true, Platform: "linux/amd64"},
			},
		})
		require.NoError(t, err)
		assert.True(t, imageExporterCalled.Load(), "image exporter should be called for push")
		assert.True(t, dockerExporterCalled.Load(), "docker exporter should be called for export-docker")
	})

	t.Run("nil definition returns error", func(t *testing.T) {
		solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			return &client.SolveResponse{}, nil
		}}

		err := Run(context.Background(), RunInput{
			Solver:  solver,
			Jobs:    []Job{{Name: "build", Definition: def}},
			Display: &fakeDisplay{},
			ImagePublishes: []ImagePublish{
				{Definition: nil, JobName: "build", Image: "myapp:dev", ExportDocker: true, Platform: "linux/amd64"},
			},
		})
		require.ErrorIs(t, err, ErrNilDefinition)
	})
}

func TestTeeStatus(t *testing.T) {
	t.Parallel()

	t.Run("nil collector returns source", func(t *testing.T) {
		t.Parallel()

		ch := make(chan *client.SolveStatus, 1)
		result := teeStatus(context.Background(), ch, nil, "step")
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

		out := teeStatus(context.Background(), src, collector, "build")

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
			out := teeStatus(ctx, src, collector, "step")

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

	var skippedJob string
	display := &fakeDisplay{
		skipFn: func(_ context.Context, jobName string) { skippedJob = jobName },
	}

	wctx := &conditional.Context{
		Getenv:      func(string) string { return "" },
		Branch:      "develop",
		Tag:         "",
		PipelineEnv: map[string]string{},
	}

	depNode := &dagNode{
		job:     Job{Name: "build", Definition: &llb.Definition{Def: [][]byte{{}}}},
		done:    make(chan struct{}),
		outputs: map[string]string{"deploy": "no"},
	}
	close(depNode.done)

	whenMock := &MockCondition{}
	whenMock.On("EvaluateDeferred", mock.Anything, mock.Anything).Return(false, nil)

	node := &dagNode{
		job: Job{
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
		display: display,
		nodes:   nodes,
		sem:     semaphore.NewWeighted(1),
		whenCtx: wctx,
	}

	err := runNode(context.Background(), node, cfg)
	require.NoError(t, err)
	assert.True(t, node.skipped)
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

	var skippedJobs []string
	display := &fakeDisplay{
		skipFn: func(_ context.Context, jobName string) { skippedJobs = append(skippedJobs, jobName) },
	}

	depNode := &dagNode{
		job:     Job{Name: "build", Definition: &llb.Definition{Def: [][]byte{{}}}},
		done:    make(chan struct{}),
		skipped: true,
	}
	close(depNode.done)

	// Downstream node has a deferred When that should never be called
	// because its dependency was skipped.
	whenMock := &MockCondition{}

	node := &dagNode{
		job: Job{
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
		display: display,
		nodes:   nodes,
		sem:     semaphore.NewWeighted(1),
		whenCtx: &conditional.Context{
			Getenv:      func(string) string { return "" },
			PipelineEnv: map[string]string{},
		},
	}

	err := runNode(context.Background(), node, cfg)
	require.NoError(t, err)
	assert.True(t, node.skipped)
	assert.Equal(t, []string{"deploy"}, skippedJobs)
	// EvaluateDeferred must not be called when dep is skipped.
	whenMock.AssertNotCalled(t, "EvaluateDeferred", mock.Anything, mock.Anything)
}

func TestRunNodeSkippedStepsReported(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	solver := &fakeSolver{
		solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			return &client.SolveResponse{}, nil
		},
	}

	var mu sync.Mutex
	var reported []string
	display := &fakeDisplay{
		skipStepFn: func(_ context.Context, jobName, stepName string) {
			mu.Lock()
			reported = append(reported, jobName+"/"+stepName)
			mu.Unlock()
		},
	}

	node := &dagNode{
		job: Job{
			Name:         "build",
			Definition:   def,
			SkippedSteps: []string{"slow-test", "lint"},
		},
		done: make(chan struct{}),
	}

	nodes := map[string]*dagNode{"build": node}

	cfg := runConfig{
		solver:  solver,
		display: display,
		nodes:   nodes,
		sem:     semaphore.NewWeighted(1),
	}

	err = runNode(context.Background(), node, cfg)
	require.NoError(t, err)
	assert.Equal(t, []string{"build/slow-test", "build/lint"}, reported)
}

func TestRunNodeOutputExtractionFailure(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	outputDef, err := llb.Scratch().Marshal(context.Background())
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

	display := &fakeDisplay{}

	node := &dagNode{
		job: Job{
			Name:       "build",
			Definition: def,
			OutputDef:  outputDef,
		},
		done: make(chan struct{}),
	}

	nodes := map[string]*dagNode{"build": node}

	cfg := runConfig{
		solver:  solver,
		display: display,
		nodes:   nodes,
		sem:     semaphore.NewWeighted(1),
	}

	err = runNode(context.Background(), node, cfg)
	require.Error(t, err)
	require.ErrorIs(t, err, extractionErr)
	require.Contains(t, err.Error(), "output extraction")
	require.NotNil(t, node.err)
}
