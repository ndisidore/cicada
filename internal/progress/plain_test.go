package progress

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlainRun(t *testing.T) {
	t.Parallel()

	now := time.Now()
	completed := now.Add(500 * time.Millisecond)

	tests := []struct {
		name         string
		statuses     []*client.SolveStatus
		wantLogs     []string
		wantLogCount map[string]int // exact occurrence count for specific substrings
		wantEmpty    bool           // assert log buffer is empty
		wantErr      bool
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
			wantLogs: []string{"vertex error"},
			wantErr:  true,
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
			wantLogs: []string{"output"},
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
			wantLogs:     []string{"started"},
			wantLogCount: map[string]int{"started": 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

			ch := make(chan *client.SolveStatus, len(tt.statuses))
			for _, s := range tt.statuses {
				ch <- s
			}
			close(ch)

			p := &Plain{Log: log}
			err := p.Run(context.Background(), "test-step", ch)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			output := buf.String()

			if tt.wantEmpty {
				assert.Empty(t, output)
			}

			for _, want := range tt.wantLogs {
				assert.Contains(t, output, want)
			}

			for substr, count := range tt.wantLogCount {
				actual := strings.Count(output, substr)
				assert.Equal(t, count, actual, "expected %q to appear %d time(s), got %d", substr, count, actual)
			}
		})
	}
}
