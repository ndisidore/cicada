package runner

import (
	"testing"

	"github.com/moby/buildkit/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type wantWarning struct {
	short  string
	detail []string
}

func TestRedactStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		status       *client.SolveStatus
		secrets      map[string]string
		wantLogs     []string
		wantErrors   []string
		wantWarnings []wantWarning
	}{
		{
			name: "single secret in log data",
			status: &client.SolveStatus{
				Logs: []*client.VertexLog{
					{Data: []byte("connecting to token=s3cret host")},
				},
			},
			secrets:  map[string]string{"TOKEN": "s3cret"},
			wantLogs: []string{"connecting to token=*** host"},
		},
		{
			name: "multiple secrets",
			status: &client.SolveStatus{
				Logs: []*client.VertexLog{
					{Data: []byte("user=admin pass=hunter2")},
				},
			},
			secrets:  map[string]string{"USER": "admin", "PASS": "hunter2"},
			wantLogs: []string{"user=*** pass=***"},
		},
		{
			name: "secret in vertex error",
			status: &client.SolveStatus{
				Vertexes: []*client.Vertex{
					{Error: "auth failed for token abc123"},
				},
			},
			secrets:    map[string]string{"TOKEN": "abc123"},
			wantErrors: []string{"auth failed for token ***"},
		},
		{
			name: "empty secrets map",
			status: &client.SolveStatus{
				Logs: []*client.VertexLog{
					{Data: []byte("unchanged log line")},
				},
			},
			secrets:  map[string]string{},
			wantLogs: []string{"unchanged log line"},
		},
		{
			name:    "nil status",
			status:  nil,
			secrets: map[string]string{"KEY": "val"},
		},
		{
			name: "empty value in secrets is skipped",
			status: &client.SolveStatus{
				Logs: []*client.VertexLog{
					{Data: []byte("nothing should change")},
				},
			},
			secrets:  map[string]string{"EMPTY": ""},
			wantLogs: []string{"nothing should change"},
		},
		{
			name: "secret appears multiple times in same log",
			status: &client.SolveStatus{
				Logs: []*client.VertexLog{
					{Data: []byte("key=secret and again secret end")},
				},
			},
			secrets:  map[string]string{"S": "secret"},
			wantLogs: []string{"key=*** and again *** end"},
		},
		{
			name: "longer secret replaced before shorter prefix",
			status: &client.SolveStatus{
				Logs: []*client.VertexLog{
					{Data: []byte("value=abcdef")},
				},
			},
			secrets:  map[string]string{"SHORT": "abc", "LONG": "abcdef"},
			wantLogs: []string{"value=***"},
		},
		{
			name: "secret in warning short and detail",
			status: &client.SolveStatus{
				Warnings: []*client.VertexWarning{
					{
						Short:  []byte("token s3cret leaked"),
						Detail: [][]byte{[]byte("detail has s3cret too"), []byte("clean line")},
					},
				},
			},
			secrets: map[string]string{"TOKEN": "s3cret"},
			wantWarnings: []wantWarning{
				{short: "token *** leaked", detail: []string{"detail has *** too", "clean line"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			redactStatus(tt.status, tt.secrets)

			if tt.status == nil {
				return
			}

			require.Len(t, tt.status.Logs, len(tt.wantLogs))
			for i, want := range tt.wantLogs {
				assert.Equal(t, want, string(tt.status.Logs[i].Data))
			}

			require.Len(t, tt.status.Vertexes, len(tt.wantErrors))
			for i, want := range tt.wantErrors {
				assert.Equal(t, want, tt.status.Vertexes[i].Error)
			}

			require.Len(t, tt.status.Warnings, len(tt.wantWarnings))
			for i, want := range tt.wantWarnings {
				assert.Equal(t, want.short, string(tt.status.Warnings[i].Short))
				require.Len(t, tt.status.Warnings[i].Detail, len(want.detail))
				for j, d := range want.detail {
					assert.Equal(t, d, string(tt.status.Warnings[i].Detail[j]))
				}
			}
		})
	}
}
