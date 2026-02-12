package cache

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/moby/buildkit/client"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollector(t *testing.T) {
	t.Parallel()

	now := time.Now()
	started := now.Add(-time.Second)
	completed := now

	t.Run("Observe", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name      string
			statuses  []*client.SolveStatus
			wantOps   int
			wantCache int
		}{
			{
				name: "counts completed vertices",
				statuses: []*client.SolveStatus{
					{Vertexes: []*client.Vertex{
						{Digest: digest.FromString("v1"), Name: "op1", Started: &started, Completed: &completed, Cached: true},
						{Digest: digest.FromString("v2"), Name: "op2", Started: &started, Completed: &completed, Cached: false},
					}},
				},
				wantOps:   2,
				wantCache: 1,
			},
			{
				name: "skips incomplete vertices",
				statuses: []*client.SolveStatus{
					{Vertexes: []*client.Vertex{
						{Digest: digest.FromString("v1"), Name: "op1", Started: &started, Completed: nil},
						{Digest: digest.FromString("v2"), Name: "op2", Started: nil, Completed: &completed},
						{Digest: digest.FromString("v3"), Name: "op3", Started: &started, Completed: &completed, Cached: true},
					}},
				},
				wantOps:   1,
				wantCache: 1,
			},
			{
				name:      "nil status is a no-op",
				statuses:  []*client.SolveStatus{nil},
				wantOps:   0,
				wantCache: 0,
			},
			{
				name: "nil vertex entry skipped",
				statuses: []*client.SolveStatus{
					{Vertexes: []*client.Vertex{nil, {Digest: digest.FromString("v1"), Name: "op1", Started: &started, Completed: &completed}}},
				},
				wantOps:   1,
				wantCache: 0,
			},
			{
				name: "empty digest skipped",
				statuses: []*client.SolveStatus{
					{Vertexes: []*client.Vertex{
						{Name: "op1", Started: &started, Completed: &completed, Cached: true},
					}},
				},
				wantOps:   0,
				wantCache: 0,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				c := NewCollector()
				for _, s := range tt.statuses {
					c.Observe("job1", s)
				}
				r := c.Report()
				if tt.wantOps == 0 {
					assert.Empty(t, r.Jobs)
					return
				}
				require.Len(t, r.Jobs, 1)
				assert.Equal(t, tt.wantOps, r.Jobs[0].TotalOps)
				assert.Equal(t, tt.wantCache, r.Jobs[0].CachedOps)
			})
		}
	})

	t.Run("Concurrent", func(t *testing.T) {
		t.Parallel()

		c := NewCollector()
		var wg sync.WaitGroup
		for i := range 10 {
			wg.Go(func() {
				c.Observe("job", &client.SolveStatus{
					Vertexes: []*client.Vertex{
						{Digest: digest.FromString(fmt.Sprintf("v%d", i)), Name: "op", Started: &started, Completed: &completed, Cached: i%2 == 0},
					},
				})
			})
		}
		wg.Wait()

		r := c.Report()
		require.Len(t, r.Jobs, 1)
		assert.Equal(t, 10, r.Jobs[0].TotalOps)
	})

	t.Run("MultipleJobs", func(t *testing.T) {
		t.Parallel()

		c := NewCollector()
		c.Observe("build", &client.SolveStatus{
			Vertexes: []*client.Vertex{
				{Digest: digest.FromString("v1"), Name: "op1", Started: &started, Completed: &completed, Cached: true},
			},
		})
		c.Observe("test", &client.SolveStatus{
			Vertexes: []*client.Vertex{
				{Digest: digest.FromString("v2"), Name: "op2", Started: &started, Completed: &completed, Cached: false},
			},
		})

		r := c.Report()
		assert.Len(t, r.Jobs, 2)
	})

	t.Run("DeduplicatesByDigest", func(t *testing.T) {
		t.Parallel()

		d1 := digest.FromString("vertex-1")
		d2 := digest.FromString("vertex-2")

		c := NewCollector()
		// First update: both vertices complete.
		c.Observe("build", &client.SolveStatus{
			Vertexes: []*client.Vertex{
				{Digest: d1, Name: "op1", Started: &started, Completed: &completed, Cached: true},
				{Digest: d2, Name: "op2", Started: &started, Completed: &completed, Cached: false},
			},
		})
		// Second update: BuildKit re-sends the same completed vertices.
		c.Observe("build", &client.SolveStatus{
			Vertexes: []*client.Vertex{
				{Digest: d1, Name: "op1", Started: &started, Completed: &completed, Cached: true},
				{Digest: d2, Name: "op2", Started: &started, Completed: &completed, Cached: false},
			},
		})

		r := c.Report()
		require.Len(t, r.Jobs, 1)
		assert.Equal(t, 2, r.Jobs[0].TotalOps, "duplicate vertices should be counted once")
		assert.Equal(t, 1, r.Jobs[0].CachedOps)
	})
}

func TestReport(t *testing.T) {
	t.Parallel()

	t.Run("HitRate", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name string
			r    Report
			want float64
		}{
			{
				name: "50 percent",
				r: Report{Jobs: []JobReport{
					{TotalOps: 4, CachedOps: 2},
				}},
				want: 0.5,
			},
			{
				name: "100 percent",
				r: Report{Jobs: []JobReport{
					{TotalOps: 3, CachedOps: 3},
				}},
				want: 1.0,
			},
			{
				name: "0 percent",
				r: Report{Jobs: []JobReport{
					{TotalOps: 5, CachedOps: 0},
				}},
				want: 0.0,
			},
			{
				name: "empty report no division by zero",
				r:    Report{},
				want: 0.0,
			},
			{
				name: "multi-job aggregation",
				r: Report{Jobs: []JobReport{
					{TotalOps: 6, CachedOps: 4},
					{TotalOps: 3, CachedOps: 0},
				}},
				want: 4.0 / 9.0,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				assert.InDelta(t, tt.want, tt.r.HitRate(), 1e-9)
			})
		}
	})

	t.Run("PrintReport", func(t *testing.T) {
		t.Parallel()

		r := Report{Jobs: []JobReport{
			{JobName: "build", TotalOps: 6, CachedOps: 4, Duration: 1200 * time.Millisecond},
			{JobName: "test", TotalOps: 3, CachedOps: 0, Duration: 8400 * time.Millisecond},
		}}

		var buf bytes.Buffer
		PrintReport(&buf, r)
		out := buf.String()
		assert.Contains(t, out, "Cache summary:")
		assert.Contains(t, out, "build")
		assert.Contains(t, out, "4/6 cached")
		assert.Contains(t, out, "test")
		assert.Contains(t, out, "0/3 cached")
		assert.Contains(t, out, "Overall: 4/9 cached")
	})
}
