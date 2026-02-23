package plain_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndisidore/cicada/internal/progress/plain"
	"github.com/ndisidore/cicada/internal/progress/progressmodel"
	"github.com/ndisidore/cicada/pkg/slogctx"
)

func TestDisplayStatus(t *testing.T) {
	t.Parallel()

	now := time.Now()
	completed := now.Add(500 * time.Millisecond)

	tests := []struct {
		name         string
		statuses     []*client.SolveStatus
		wantLogs     []string
		wantLogCount map[string]int // exact occurrence count for specific substrings
		wantEmpty    bool           // assert log buffer is empty
	}{
		{
			name: "vertex started then completed",
			statuses: []*client.SolveStatus{
				{
					Vertexes: []*client.Vertex{
						{Digest: digest.FromString("v1"), Name: "step1", Started: &now},
					},
				},
				{
					Vertexes: []*client.Vertex{
						{Digest: digest.FromString("v1"), Name: "step1", Started: &now, Completed: &completed},
					},
				},
			},
			wantLogs: []string{"started", "done"},
		},
		{
			name: "cached vertex",
			statuses: []*client.SolveStatus{
				{
					Vertexes: []*client.Vertex{
						{Digest: digest.FromString("v2"), Name: "step2", Cached: true},
					},
				},
			},
			wantLogs: []string{"cached"},
		},
		{
			name: "vertex error",
			statuses: []*client.SolveStatus{
				{
					Vertexes: []*client.Vertex{
						{Digest: digest.FromString("v3"), Name: "step3", Error: "something broke"},
					},
				},
			},
			wantLogs: []string{"FAIL"},
		},
		{
			name: "log output",
			statuses: []*client.SolveStatus{
				{
					Logs: []*client.VertexLog{
						{Data: []byte("hello world\n")},
					},
				},
			},
			wantLogs: []string{"hello world"},
		},
		{
			name: "empty log data skipped",
			statuses: []*client.SolveStatus{
				{
					Logs: []*client.VertexLog{
						{Data: []byte("")},
						{Data: []byte("\n")},
					},
				},
			},
			wantEmpty: true,
		},
		{
			name: "duplicate vertex transitions suppressed",
			statuses: []*client.SolveStatus{
				{
					Vertexes: []*client.Vertex{
						{Digest: digest.FromString("v4"), Name: "step4", Started: &now},
					},
				},
				{
					Vertexes: []*client.Vertex{
						{Digest: digest.FromString("v4"), Name: "step4", Started: &now},
					},
				},
			},
			wantLogs:     []string{"started step4"},
			wantLogCount: map[string]int{"started step4": 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			ctx := slogctx.ContextWithLogger(t.Context(), logger)

			p := plain.New()
			require.NoError(t, p.Start(ctx))

			p.Send(progressmodel.JobAddedMsg{Job: "test-step"})
			for _, s := range tt.statuses {
				p.Send(progressmodel.JobStatusMsg{Job: "test-step", Status: s})
			}
			p.Send(progressmodel.JobDoneMsg{Job: "test-step"})
			require.NoError(t, p.Shutdown())

			output := buf.String()

			if tt.wantEmpty {
				assert.Empty(t, output)
			} else {
				for _, want := range tt.wantLogs {
					assert.Contains(t, output, want)
				}
				for substr, count := range tt.wantLogCount {
					actual := strings.Count(output, substr)
					assert.Equal(t, count, actual, "expected %q to appear %d time(s), got %d", substr, count, actual)
				}
			}
		})
	}
}

func TestDisplaySkipStep(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx := slogctx.ContextWithLogger(t.Context(), logger)

	p := plain.New()
	require.NoError(t, p.Start(ctx))
	p.Send(progressmodel.StepSkippedMsg{Job: "deploy", Step: "notify"})
	require.NoError(t, p.Shutdown())

	output := buf.String()
	assert.Contains(t, output, "step skipped")
	assert.Contains(t, output, "deploy")
	assert.Contains(t, output, "notify")
	assert.Contains(t, output, "step.skipped")
}

func TestDisplaySkip(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx := slogctx.ContextWithLogger(t.Context(), logger)

	p := plain.New()
	require.NoError(t, p.Start(ctx))
	p.Send(progressmodel.JobSkippedMsg{Job: "deploy"})
	require.NoError(t, p.Shutdown())

	output := buf.String()
	assert.Contains(t, output, "job skipped")
	assert.Contains(t, output, "deploy")
	assert.Contains(t, output, "job.skipped")
}
