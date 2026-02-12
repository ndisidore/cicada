package cache

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/moby/buildkit/client"
)

// VertexStat records whether a single vertex operation was cached and its duration.
type VertexStat struct {
	Name     string
	Cached   bool
	Duration time.Duration
}

// JobReport summarizes cache statistics for a single pipeline job.
type JobReport struct {
	JobName   string
	TotalOps  int
	CachedOps int
	Duration  time.Duration
}

// Report aggregates cache statistics across all jobs.
type Report struct {
	Jobs []JobReport
}

// HitRate returns the overall cache hit ratio (0.0-1.0).
// Returns 0 when there are no operations.
func (r Report) HitRate() float64 {
	var total, cached int
	for i := range r.Jobs {
		total += r.Jobs[i].TotalOps
		cached += r.Jobs[i].CachedOps
	}
	if total == 0 {
		return 0
	}
	return float64(cached) / float64(total)
}

// Collector accumulates vertex statistics from SolveStatus events.
// It is safe for concurrent use.
type Collector struct {
	mu    sync.Mutex
	order []string                       // job names in first-observed order
	stats map[string][]VertexStat        // job name -> vertex stats
	seen  map[string]map[string]struct{} // job name -> seen digests
}

// NewCollector returns a new Collector ready for use.
func NewCollector() *Collector {
	return &Collector{
		stats: make(map[string][]VertexStat),
		seen:  make(map[string]map[string]struct{}),
	}
}

// Observe records vertex information from a SolveStatus event for the named job.
// Vertices with empty digests are skipped. Vertices are deduplicated by digest;
// BuildKit may stream the same completed vertex in multiple status updates.
func (c *Collector) Observe(jobName string, status *client.SolveStatus) {
	if status == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen[jobName] == nil {
		c.seen[jobName] = make(map[string]struct{})
	}
	for _, v := range status.Vertexes {
		if v == nil || v.Started == nil || v.Completed == nil {
			continue
		}
		dig := v.Digest.String()
		if dig == "" {
			continue
		}
		if _, dup := c.seen[jobName][dig]; dup {
			continue
		}
		c.seen[jobName][dig] = struct{}{}
		if c.stats[jobName] == nil {
			c.order = append(c.order, jobName)
		}
		c.stats[jobName] = append(c.stats[jobName], VertexStat{
			Name:     v.Name,
			Cached:   v.Cached,
			Duration: v.Completed.Sub(*v.Started),
		})
	}
}

// Report returns the aggregated cache statistics in execution order.
// Call after all jobs complete.
func (c *Collector) Report() Report {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := Report{Jobs: make([]JobReport, 0, len(c.order))}
	for _, name := range c.order {
		stats := c.stats[name]
		sr := JobReport{JobName: name, TotalOps: len(stats)}
		for i := range stats {
			if stats[i].Cached {
				sr.CachedOps++
			}
			sr.Duration += stats[i].Duration
		}
		r.Jobs = append(r.Jobs, sr)
	}
	return r
}

// PrintReport writes a human-readable cache summary to w.
func PrintReport(w io.Writer, r Report) {
	_, _ = fmt.Fprintln(w, "Cache summary:")
	var totalOps, totalCached int
	for _, sr := range r.Jobs {
		totalOps += sr.TotalOps
		totalCached += sr.CachedOps
		pct := 0.0
		if sr.TotalOps > 0 {
			pct = float64(sr.CachedOps) / float64(sr.TotalOps) * 100
		}
		_, _ = fmt.Fprintf(w, "  %-16s %d/%d cached (%4.1f%%)  %s\n",
			sr.JobName, sr.CachedOps, sr.TotalOps, pct, sr.Duration.Round(time.Millisecond))
	}
	pct := 0.0
	if totalOps > 0 {
		pct = float64(totalCached) / float64(totalOps) * 100
	}
	_, _ = fmt.Fprintf(w, "  Overall: %d/%d cached (%4.1f%%)\n", totalCached, totalOps, pct)
}
