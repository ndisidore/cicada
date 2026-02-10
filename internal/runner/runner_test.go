package runner

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
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
