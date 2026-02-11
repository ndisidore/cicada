package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestResolverDetect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		setup    func(tb testing.TB) *Resolver
		wantType Type
		wantErr  error
	}{
		{
			name: "first available wins",
			setup: func(tb testing.TB) *Resolver {
				tb.Helper()
				docker := new(MockRuntime)
				docker.On("Available", mock.Anything).Return(true).Once()
				docker.On("Type").Return(Docker).Once()
				tb.Cleanup(func() { docker.AssertExpectations(tb) })

				podman := new(MockRuntime)
				tb.Cleanup(func() { podman.AssertExpectations(tb) })

				return &Resolver{Factories: []func() Runtime{
					func() Runtime { return docker },
					func() Runtime { return podman },
				}}
			},
			wantType: Docker,
		},
		{
			name: "skips unavailable",
			setup: func(tb testing.TB) *Resolver {
				tb.Helper()
				docker := new(MockRuntime)
				docker.On("Available", mock.Anything).Return(false).Once()
				tb.Cleanup(func() { docker.AssertExpectations(tb) })

				podman := new(MockRuntime)
				podman.On("Available", mock.Anything).Return(true).Once()
				podman.On("Type").Return(Podman).Once()
				tb.Cleanup(func() { podman.AssertExpectations(tb) })

				return &Resolver{Factories: []func() Runtime{
					func() Runtime { return docker },
					func() Runtime { return podman },
				}}
			},
			wantType: Podman,
		},
		{
			name: "none available returns ErrNoRuntime",
			setup: func(tb testing.TB) *Resolver {
				tb.Helper()
				docker := new(MockRuntime)
				docker.On("Available", mock.Anything).Return(false).Once()
				tb.Cleanup(func() { docker.AssertExpectations(tb) })

				podman := new(MockRuntime)
				podman.On("Available", mock.Anything).Return(false).Once()
				tb.Cleanup(func() { podman.AssertExpectations(tb) })

				return &Resolver{Factories: []func() Runtime{
					func() Runtime { return docker },
					func() Runtime { return podman },
				}}
			},
			wantErr: ErrNoRuntime,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := tt.setup(t)
			rt, err := r.Detect(context.Background())
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, rt.Type())
		})
	}
}

func TestResolverGet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		rtName   string
		wantType Type
		wantErr  error
	}{
		{
			name:     "explicit docker",
			rtName:   string(Docker),
			wantType: Docker,
		},
		{
			name:     "explicit podman",
			rtName:   string(Podman),
			wantType: Podman,
		},
		{
			name:    "unknown runtime",
			rtName:  "containerd",
			wantErr: ErrUnknownRuntime,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &Resolver{}
			rt, err := r.Get(context.Background(), tt.rtName)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, rt.Type())
		})
	}
}

func TestResolverGetAuto(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		setup    func(tb testing.TB) *Resolver
		wantType Type
		wantErr  error
	}{
		{
			name:  "auto detects first available",
			input: "auto",
			setup: func(tb testing.TB) *Resolver {
				tb.Helper()
				docker := new(MockRuntime)
				docker.On("Available", mock.Anything).Return(true).Once()
				docker.On("Type").Return(Docker).Once()
				tb.Cleanup(func() { docker.AssertExpectations(tb) })

				return &Resolver{Factories: []func() Runtime{
					func() Runtime { return docker },
				}}
			},
			wantType: Docker,
		},
		{
			name:  "empty string triggers auto-detect",
			input: "",
			setup: func(tb testing.TB) *Resolver {
				tb.Helper()
				podman := new(MockRuntime)
				podman.On("Available", mock.Anything).Return(true).Once()
				podman.On("Type").Return(Podman).Once()
				tb.Cleanup(func() { podman.AssertExpectations(tb) })

				return &Resolver{Factories: []func() Runtime{
					func() Runtime { return podman },
				}}
			},
			wantType: Podman,
		},
		{
			name:  "auto with none available",
			input: "auto",
			setup: func(tb testing.TB) *Resolver {
				tb.Helper()
				docker := new(MockRuntime)
				docker.On("Available", mock.Anything).Return(false).Once()
				tb.Cleanup(func() { docker.AssertExpectations(tb) })

				return &Resolver{Factories: []func() Runtime{
					func() Runtime { return docker },
				}}
			},
			wantErr: ErrNoRuntime,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := tt.setup(t)
			rt, err := r.Get(context.Background(), tt.input)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, rt.Type())
		})
	}
}
