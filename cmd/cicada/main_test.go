package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/ndisidore/cicada/internal/builder"
	"github.com/ndisidore/cicada/internal/runner"
	"github.com/ndisidore/cicada/pkg/pipeline"
)

// fakeEngine implements Engine for testing.
type fakeEngine struct {
	ensureRunningFn func(ctx context.Context, addr string) (string, error)
	startFn         func(ctx context.Context) (string, error)
	stopFn          func(ctx context.Context) error
	removeFn        func(ctx context.Context) error
	statusFn        func(ctx context.Context) (string, error)
}

func (f *fakeEngine) EnsureRunning(ctx context.Context, addr string) (string, error) {
	return f.ensureRunningFn(ctx, addr)
}

func (f *fakeEngine) Start(ctx context.Context) (string, error) {
	return f.startFn(ctx)
}

func (f *fakeEngine) Stop(ctx context.Context) error {
	return f.stopFn(ctx)
}

func (f *fakeEngine) Remove(ctx context.Context) error {
	return f.removeFn(ctx)
}

func (f *fakeEngine) Status(ctx context.Context) (string, error) {
	return f.statusFn(ctx)
}

func TestEngineStartAction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		startFn func(ctx context.Context) (string, error)
		wantOut string
		wantErr bool
	}{
		{
			name: "success",
			startFn: func(_ context.Context) (string, error) {
				return "tcp://127.0.0.1:1234", nil
			},
			wantOut: "BuildKit engine started at tcp://127.0.0.1:1234\n",
		},
		{
			name: "error propagates",
			startFn: func(_ context.Context) (string, error) {
				return "", errors.New("docker not found")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			a := &app{
				engine: &fakeEngine{startFn: tt.startFn},
				stdout: &buf,
			}
			err := a.engineStartAction(context.Background(), &cli.Command{})
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantOut, buf.String())
		})
	}
}

func TestEngineStopAction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		stopFn   func(ctx context.Context) error
		removeFn func(ctx context.Context) error
		wantOut  string
		wantErr  bool
	}{
		{
			name: "success",
			stopFn: func(_ context.Context) error {
				return nil
			},
			removeFn: func(_ context.Context) error {
				return nil
			},
			wantOut: "BuildKit engine stopped\n",
		},
		{
			name: "stop error propagates",
			stopFn: func(_ context.Context) error {
				return errors.New("stop failed")
			},
			removeFn: func(_ context.Context) error {
				return nil
			},
			wantErr: true,
		},
		{
			name: "remove error propagates",
			stopFn: func(_ context.Context) error {
				return nil
			},
			removeFn: func(_ context.Context) error {
				return errors.New("remove failed")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			a := &app{
				engine: &fakeEngine{stopFn: tt.stopFn, removeFn: tt.removeFn},
				stdout: &buf,
			}
			err := a.engineStopAction(context.Background(), &cli.Command{})
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantOut, buf.String())
		})
	}
}

func TestEngineStatusAction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		statusFn func(ctx context.Context) (string, error)
		wantOut  string
		wantErr  bool
	}{
		{
			name: "running",
			statusFn: func(_ context.Context) (string, error) {
				return "running", nil
			},
			wantOut: "BuildKit engine: running\n",
		},
		{
			name: "not running",
			statusFn: func(_ context.Context) (string, error) {
				return "", nil
			},
			wantOut: "BuildKit engine: not running\n",
		},
		{
			name: "error propagates",
			statusFn: func(_ context.Context) (string, error) {
				return "", errors.New("docker not found")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			a := &app{
				engine: &fakeEngine{statusFn: tt.statusFn},
				stdout: &buf,
			}
			err := a.engineStatusAction(context.Background(), &cli.Command{})
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantOut, buf.String())
		})
	}
}

// newTestCommand creates a minimal cli.Command with the given flags for testing.
func newTestCommand(flags []cli.Flag) *cli.Command {
	return &cli.Command{
		Flags:    flags,
		HideHelp: true,
		Action:   func(context.Context, *cli.Command) error { return nil },
	}
}

func TestResolveAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		addr            string
		noDaemon        bool
		ensureRunningFn func(ctx context.Context, addr string) (string, error)
		wantAddr        string
		wantErr         bool
	}{
		{
			name:     "no-daemon returns addr as-is",
			addr:     "tcp://custom:9999",
			noDaemon: true,
			wantAddr: "tcp://custom:9999",
		},
		{
			name: "calls EnsureRunning for default",
			addr: "tcp://127.0.0.1:1234",
			ensureRunningFn: func(_ context.Context, addr string) (string, error) {
				return addr, nil
			},
			wantAddr: "tcp://127.0.0.1:1234",
		},
		{
			name: "EnsureRunning error propagates",
			addr: "tcp://127.0.0.1:1234",
			ensureRunningFn: func(_ context.Context, _ string) (string, error) {
				return "", errors.New("cannot start daemon")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := &app{
				engine: &fakeEngine{ensureRunningFn: tt.ensureRunningFn},
				stdout: &bytes.Buffer{},
			}

			flags := []cli.Flag{
				&cli.StringFlag{Name: "addr", Value: tt.addr},
				&cli.BoolFlag{Name: "no-daemon", Value: tt.noDaemon},
			}
			cmd := newTestCommand(flags)
			// Parse with empty args to initialize flag values.
			require.NoError(t, cmd.Run(context.Background(), []string{"test"}))

			addr, err := a.resolveAddr(context.Background(), cmd)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantAddr, addr)
		})
	}
}

func TestBuildRunnerJobs(t *testing.T) {
	t.Parallel()

	def, err := llb.Scratch().Marshal(context.Background())
	require.NoError(t, err)

	tests := []struct {
		name         string
		result       builder.Result
		pipeline     pipeline.Pipeline
		want         []runner.Job
		wantSentinel error
	}{
		{
			name: "matching order",
			result: builder.Result{
				Definitions: []*llb.Definition{def, def},
				JobNames:    []string{"a", "b"},
			},
			pipeline: pipeline.Pipeline{
				Jobs: []pipeline.Job{
					{Name: "a"},
					{Name: "b", DependsOn: []string{"a"}},
				},
			},
			want: []runner.Job{
				{Name: "a", Definition: def},
				{Name: "b", Definition: def, DependsOn: []string{"a"}},
			},
		},
		{
			name: "reordered builder output wires deps correctly",
			result: builder.Result{
				Definitions: []*llb.Definition{def, def},
				JobNames:    []string{"b", "a"},
			},
			pipeline: pipeline.Pipeline{
				Jobs: []pipeline.Job{
					{Name: "a"},
					{Name: "b", DependsOn: []string{"a"}},
				},
			},
			want: []runner.Job{
				{Name: "b", Definition: def, DependsOn: []string{"a"}},
				{Name: "a", Definition: def},
			},
		},
		{
			name: "definitions and job names length mismatch",
			result: builder.Result{
				Definitions: []*llb.Definition{def},
				JobNames:    []string{"a", "b"},
			},
			pipeline:     pipeline.Pipeline{},
			wantSentinel: errResultMismatch,
		},
		{
			name: "unknown builder job name",
			result: builder.Result{
				Definitions: []*llb.Definition{def},
				JobNames:    []string{"unknown"},
			},
			pipeline: pipeline.Pipeline{
				Jobs: []pipeline.Job{
					{Name: "a"},
				},
			},
			wantSentinel: errUnknownBuilderJob,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := buildRunnerJobs(tt.result, tt.pipeline)
			if tt.wantSentinel != nil {
				require.ErrorIs(t, err, tt.wantSentinel)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
