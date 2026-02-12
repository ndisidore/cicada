package runner

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndisidore/cicada/internal/builder"
	"github.com/ndisidore/cicada/internal/cache"
	"github.com/ndisidore/cicada/pkg/pipeline"
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
	runFn func(ctx context.Context, name string, ch <-chan *client.SolveStatus) error
}

func (f *fakeDisplay) Run(ctx context.Context, name string, ch <-chan *client.SolveStatus) error {
	if f.runFn != nil {
		return f.runFn(ctx, name, ch)
	}
	for s := range ch {
		_ = s
	}
	return nil
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
	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	tests := []struct {
		name         string
		input        RunInput
		wantErr      string
		wantSentinel error
	}{
		{
			name: "single step solves successfully",
			input: RunInput{
				Solver: &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
					close(ch)
					return &client.SolveResponse{}, nil
				}},
				Steps:   []Step{{Name: "build", Definition: def}},
				Display: &fakeDisplay{},
			},
		},
		{
			name: "multi-step execution",
			input: RunInput{
				Solver: &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
					close(ch)
					return &client.SolveResponse{}, nil
				}},
				Steps: []Step{
					{Name: "first", Definition: def},
					{Name: "second", Definition: def},
					{Name: "third", Definition: def},
				},
				Display: &fakeDisplay{},
			},
		},
		{
			name: "solve error wraps with step name",
			input: RunInput{
				Solver: &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
					close(ch)
					return nil, errors.New("connection refused")
				}},
				Steps:   []Step{{Name: "deploy", Definition: def}},
				Display: &fakeDisplay{},
			},
			wantErr: `step "deploy"`,
		},
		{
			name: "display error propagates",
			input: RunInput{
				Solver: &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
					close(ch)
					return &client.SolveResponse{}, nil
				}},
				Steps: []Step{{Name: "render", Definition: def}},
				Display: &fakeDisplay{runFn: func(_ context.Context, _ string, ch <-chan *client.SolveStatus) error {
					for s := range ch {
						_ = s
					}
					return errors.New("terminal error")
				}},
			},
			wantErr: "displaying progress",
		},
		{
			name: "empty steps is a no-op",
			input: RunInput{
				Solver:  &fakeSolver{},
				Steps:   []Step{},
				Display: &fakeDisplay{},
			},
		},
		{
			name: "nil solver returns error",
			input: RunInput{
				Solver:  nil,
				Steps:   []Step{{Name: "x", Definition: def}},
				Display: &fakeDisplay{},
			},
			wantSentinel: ErrNilSolver,
		},
		{
			name: "nil display returns error",
			input: RunInput{
				Solver:  &fakeSolver{},
				Steps:   []Step{{Name: "x", Definition: def}},
				Display: nil,
			},
			wantSentinel: ErrNilDisplay,
		},
		{
			name: "unknown dependency",
			input: RunInput{
				Solver:  &fakeSolver{},
				Steps:   []Step{{Name: "a", Definition: def, DependsOn: []string{"nonexistent"}}},
				Display: &fakeDisplay{},
			},
			wantSentinel: ErrUnknownDep,
		},
		{
			name: "duplicate step name",
			input: RunInput{
				Solver: &fakeSolver{},
				Steps: []Step{
					{Name: "a", Definition: def},
					{Name: "a", Definition: def},
				},
				Display: &fakeDisplay{},
			},
			wantSentinel: ErrDuplicateStep,
		},
		{
			name: "mutual cycle",
			input: RunInput{
				Solver: &fakeSolver{},
				Steps: []Step{
					{Name: "a", Definition: def, DependsOn: []string{"b"}},
					{Name: "b", Definition: def, DependsOn: []string{"a"}},
				},
				Display: &fakeDisplay{},
			},
			wantSentinel: ErrCycleDetected,
		},
		{
			name: "self cycle",
			input: RunInput{
				Solver: &fakeSolver{},
				Steps: []Step{
					{Name: "a", Definition: def, DependsOn: []string{"a"}},
				},
				Display: &fakeDisplay{},
			},
			wantSentinel: ErrCycleDetected,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := Run(context.Background(), tt.input)
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

func TestRun_contextCancellation(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err = Run(ctx, RunInput{
		Solver: &fakeSolver{solveFn: func(ctx context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
			close(ch)
			return nil, ctx.Err()
		}},
		Steps:   []Step{{Name: "cancelled", Definition: def}},
		Display: &fakeDisplay{},
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestRun_dagOrdering(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	var mu sync.Mutex
	var order []string

	solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
		close(ch)
		return &client.SolveResponse{}, nil
	}}
	display := &fakeDisplay{runFn: func(_ context.Context, name string, ch <-chan *client.SolveStatus) error {
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
		for s := range ch {
			_ = s
		}
		return nil
	}}

	// Chain a -> b -> c so they must run in order.
	err = Run(context.Background(), RunInput{
		Solver: solver,
		Steps: []Step{
			{Name: "a", Definition: def},
			{Name: "b", Definition: def, DependsOn: []string{"a"}},
			{Name: "c", Definition: def, DependsOn: []string{"b"}},
		},
		Display: display,
	})
	require.NoError(t, err)
	require.Len(t, order, 3)
	idxA, idxB, idxC := indexOf(order, "a"), indexOf(order, "b"), indexOf(order, "c")
	assert.Less(t, idxA, idxB, "a must run before b")
	assert.Less(t, idxB, idxC, "b must run before c")
}

func TestRun_diamondDAG(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	var mu sync.Mutex
	completed := make(map[string]bool)

	solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
		close(ch)
		return &client.SolveResponse{}, nil
	}}
	display := &fakeDisplay{runFn: func(_ context.Context, name string, ch <-chan *client.SolveStatus) error {
		// Check that all deps completed before us.
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
		for s := range ch {
			_ = s
		}
		return nil
	}}

	// Diamond: a -> {b, c} -> d
	err = Run(context.Background(), RunInput{
		Solver: solver,
		Steps: []Step{
			{Name: "a", Definition: def},
			{Name: "b", Definition: def, DependsOn: []string{"a"}},
			{Name: "c", Definition: def, DependsOn: []string{"a"}},
			{Name: "d", Definition: def, DependsOn: []string{"b", "c"}},
		},
		Display: display,
	})
	require.NoError(t, err)
	assert.Len(t, completed, 4)
}

func TestRun_parallelIndependentSteps(t *testing.T) {
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
			Steps: []Step{
				{Name: "a", Definition: def},
				{Name: "b", Definition: def},
				{Name: "c", Definition: def},
			},
			Display: &fakeDisplay{},
		})
		require.NoError(t, err)
		assert.Equal(t, int64(3), maxConcurrent.Load(), "all 3 independent steps should run concurrently")
	})
}

func TestRun_parallelismBound(t *testing.T) {
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
			Steps: []Step{
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
}

func TestRun_errorCancelsOthers(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	var cExecuted atomic.Bool

	solver := &fakeSolver{solveFn: func(ctx context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
		close(ch)
		return &client.SolveResponse{}, nil
	}}

	// "a" completes, then "b" fails. "c" depends on "b" and should be cancelled.
	display := &fakeDisplay{runFn: func(_ context.Context, name string, ch <-chan *client.SolveStatus) error {
		if name == "c" {
			cExecuted.Store(true)
		}
		for s := range ch {
			_ = s
		}
		if name == "b" {
			return errors.New("step b exploded")
		}
		return nil
	}}

	err = Run(context.Background(), RunInput{
		Solver: solver,
		Steps: []Step{
			{Name: "a", Definition: def},
			{Name: "b", Definition: def, DependsOn: []string{"a"}},
			{Name: "c", Definition: def, DependsOn: []string{"b"}},
		},
		Display: display,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "step b exploded")
	assert.False(t, cExecuted.Load(), "step c should never execute when dep b fails")
}

func TestRun_depErrorPropagatesToGrandchild(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	var cSolved atomic.Bool

	solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
		close(ch)
		return &client.SolveResponse{}, nil
	}}
	display := &fakeDisplay{runFn: func(_ context.Context, name string, ch <-chan *client.SolveStatus) error {
		if name == "c" {
			cSolved.Store(true)
		}
		for s := range ch {
			_ = s
		}
		if name == "a" {
			return errors.New("step a failed")
		}
		return nil
	}}

	// Chain a -> b -> c. Step "a" fails; "c" should never be solved.
	err = Run(context.Background(), RunInput{
		Solver: solver,
		Steps: []Step{
			{Name: "a", Definition: def},
			{Name: "b", Definition: def, DependsOn: []string{"a"}},
			{Name: "c", Definition: def, DependsOn: []string{"b"}},
		},
		Display:     display,
		Parallelism: 1,
	})
	require.Error(t, err)
	assert.False(t, cSolved.Load(), "step c should never be solved when grandparent a fails")
}

func TestRun_nilDefinition(t *testing.T) {
	t.Parallel()

	err := Run(context.Background(), RunInput{
		Solver:  &fakeSolver{},
		Steps:   []Step{{Name: "bad", Definition: nil}},
		Display: &fakeDisplay{},
	})
	require.ErrorIs(t, err, ErrNilDefinition)
}

func TestRun_displayErrorDrain(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	// Solver writes additional statuses after display returns an error.
	// Without draining, ch blocks and Solve never returns, causing a deadlock.
	solver := &fakeSolver{solveFn: func(ctx context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
		ch <- &client.SolveStatus{}
		ch <- &client.SolveStatus{}
		ch <- &client.SolveStatus{}
		close(ch)
		return &client.SolveResponse{}, nil
	}}
	display := &fakeDisplay{runFn: func(_ context.Context, _ string, ch <-chan *client.SolveStatus) error {
		// Read one event then bail with an error, leaving events unread.
		<-ch
		return errors.New("vertex error")
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = Run(ctx, RunInput{
		Solver:  solver,
		Steps:   []Step{{Name: "drain-test", Definition: def}},
		Display: display,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "displaying progress")
}

func TestRun_failedStepUnblocksDeps(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
		close(ch)
		return &client.SolveResponse{}, nil
	}}

	// Step "a" fails. Step "b" depends on "a" and must unblock promptly.
	display := &fakeDisplay{runFn: func(_ context.Context, name string, ch <-chan *client.SolveStatus) error {
		for s := range ch {
			_ = s
		}
		if name == "a" {
			return errors.New("step a failed")
		}
		return nil
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = Run(ctx, RunInput{
		Solver: solver,
		Steps: []Step{
			{Name: "a", Definition: def},
			{Name: "b", Definition: def, DependsOn: []string{"a"}},
		},
		Display: display,
	})
	require.Error(t, err)
	// Must not be a context deadline exceeded (that would mean it hung).
	assert.NotErrorIs(t, err, context.DeadlineExceeded)
}

func TestRun_depErrorSkipsSolve(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	var bSolved atomic.Bool

	solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
		close(ch)
		return nil, errors.New("step a failed")
	}}
	display := &fakeDisplay{runFn: func(_ context.Context, name string, ch <-chan *client.SolveStatus) error {
		if name == "b" {
			bSolved.Store(true)
		}
		for s := range ch {
			_ = s
		}
		return nil
	}}

	err = Run(context.Background(), RunInput{
		Solver: solver,
		Steps: []Step{
			{Name: "a", Definition: def},
			{Name: "b", Definition: def, DependsOn: []string{"a"}},
		},
		Display:     display,
		Parallelism: 1,
	})
	require.Error(t, err)
	assert.False(t, bSolved.Load(), "step b's solve should never be invoked when dep a fails")
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

	// Pre-cancel so sem.Acquire fails immediately for all steps.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = Run(ctx, RunInput{
		Solver: solver,
		Steps: []Step{
			{Name: "a", Definition: def},
			{Name: "b", Definition: def, DependsOn: []string{"a"}},
		},
		Display:     &fakeDisplay{},
		Parallelism: 1,
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, solved.Load(), "solver should not be called when sem.Acquire fails")
}

func TestRun_solverWritesStatus(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	var received atomic.Int64
	solver := &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
		ch <- &client.SolveStatus{}
		ch <- &client.SolveStatus{}
		close(ch)
		return &client.SolveResponse{}, nil
	}}
	display := &fakeDisplay{runFn: func(_ context.Context, _ string, ch <-chan *client.SolveStatus) error {
		for s := range ch {
			_ = s
			received.Add(1)
		}
		return nil
	}}

	err = Run(context.Background(), RunInput{
		Solver:  solver,
		Steps:   []Step{{Name: "status-test", Definition: def}},
		Display: display,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), received.Load())
}

func TestRun_exports(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	tests := []struct {
		name          string
		exports       []builder.LocalExport
		solver        *fakeSolver
		display       *fakeDisplay
		wantErr       string
		wantSentinel  error
		checkOpt      bool   // whether to assert on capturedExportOpt after Run
		wantOutputDir string // expected OutputDir when checkOpt is true
	}{
		{
			name: "export solves with local exporter",
			exports: []builder.LocalExport{
				{Definition: def, StepName: "build", Local: "/tmp/out/myapp"},
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
			exports: []builder.LocalExport{
				{Definition: def, StepName: "build", Local: "/tmp/out/dist", Dir: true},
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
			name: "export solve error wraps step and path",
			exports: []builder.LocalExport{
				{Definition: def, StepName: "compile", Local: "/tmp/bin/app"},
			},
			solver: &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				close(ch)
				return nil, errors.New("disk full")
			}},
			display: &fakeDisplay{},
			wantErr: `exporting "/tmp/bin/app" from step "compile"`,
		},
		{
			name: "export display error propagates",
			exports: []builder.LocalExport{
				{Definition: def, StepName: "build", Local: "/tmp/out/myapp"},
			},
			solver: &fakeSolver{solveFn: func(_ context.Context, _ *llb.Definition, _ client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				close(ch)
				return &client.SolveResponse{}, nil
			}},
			display: &fakeDisplay{runFn: func(_ context.Context, name string, ch <-chan *client.SolveStatus) error {
				for s := range ch {
					_ = s
				}
				if strings.HasPrefix(name, "export:") {
					return errors.New("render failed")
				}
				return nil
			}},
			wantErr: "displaying export progress",
		},
		{
			name: "nil export definition returns error",
			exports: []builder.LocalExport{
				{Definition: nil, StepName: "bad", Local: "/tmp/out/x"},
			},
			solver:       &fakeSolver{},
			display:      &fakeDisplay{},
			wantSentinel: ErrNilDefinition,
		},
		{
			name: "empty export local returns error",
			exports: []builder.LocalExport{
				{Definition: def, StepName: "bad", Local: ""},
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

			// Use the same solver for both the step and export phases.
			// For step phase, always succeed.
			stepSolver := &fakeSolver{solveFn: func(ctx context.Context, d *llb.Definition, opt client.SolveOpt, ch chan *client.SolveStatus) (*client.SolveResponse, error) {
				// Export phase: capture opt and delegate to test's solver.
				if len(opt.Exports) > 0 {
					capturedExportOpt.Store(&opt)
					return tt.solver.solveFn(ctx, d, opt, ch)
				}
				close(ch)
				return &client.SolveResponse{}, nil
			}}

			err := Run(context.Background(), RunInput{
				Solver:  stepSolver,
				Steps:   []Step{{Name: "step", Definition: def}},
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
}

func TestRun_exportsConcurrent(t *testing.T) {
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
			Steps:   []Step{{Name: "build", Definition: def}},
			Display: &fakeDisplay{},
			Exports: []builder.LocalExport{
				{Definition: def, StepName: "build", Local: "/tmp/a"},
				{Definition: def, StepName: "build", Local: "/tmp/b"},
				{Definition: def, StepName: "build", Local: "/tmp/c"},
			},
		})
		require.NoError(t, err)
		assert.Equal(t, int64(3), maxConcurrent.Load(), "all 3 exports should run concurrently")
	})
}

func TestRun_exportErrorCancelsSiblings(t *testing.T) {
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
			Steps:   []Step{{Name: "build", Definition: def}},
			Display: &fakeDisplay{},
			Exports: []builder.LocalExport{
				{Definition: def, StepName: "build", Local: "/tmp/a"},
				{Definition: def, StepName: "build", Local: "/tmp/b"},
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "disk full")
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
		Steps:        []Step{{Name: "build", Definition: def}},
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

func TestTeeStatus_nilCollectorReturnsSource(t *testing.T) {
	t.Parallel()

	ch := make(chan *client.SolveStatus, 1)
	result := teeStatus(context.Background(), ch, nil, "step")
	// With nil collector, teeStatus returns the source channel directly.
	assert.Equal(t, (<-chan *client.SolveStatus)(ch), result)
}

func TestTeeStatus_forwardsAndObserves(t *testing.T) {
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
	require.Len(t, r.Steps, 1)
	assert.Equal(t, 2, r.Steps[0].TotalOps)
	assert.Equal(t, 1, r.Steps[0].CachedOps)
}

func TestTeeStatus_contextCancellation(t *testing.T) {
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
		for range out { //nolint:revive // drain remaining events
		}
	})
}
