package tracing

import (
	"context"

	"github.com/moby/buildkit/client"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Observer tracks in-flight BuildKit vertex spans under a parent job context.
// Not safe for concurrent use — intended to run within a single teeStatus goroutine.
type Observer struct {
	tracer   trace.Tracer
	jobCtx   context.Context
	inflight map[string]trace.Span
}

// NewObserver creates an Observer for a single job.
func NewObserver(jobCtx context.Context, tracer trace.Tracer) *Observer {
	return &Observer{
		tracer:   tracer,
		jobCtx:   jobCtx,
		inflight: make(map[string]trace.Span),
	}
}

// Observe processes a SolveStatus update, opening and closing vertex spans.
func (o *Observer) Observe(status *client.SolveStatus) {
	if status == nil {
		return
	}
	for _, v := range status.Vertexes {
		digest := v.Digest.String()

		if v.Started != nil {
			if _, seen := o.inflight[digest]; !seen {
				_, span := o.tracer.Start(o.jobCtx, "vertex/"+v.Name,
					trace.WithTimestamp(*v.Started),
					trace.WithAttributes(
						attribute.Bool("cached", v.Cached),
						attribute.String("vertex.name", v.Name),
						attribute.String("vertex.digest", digest),
					),
				)
				o.inflight[digest] = span
			}
		}

		if v.Completed != nil {
			span, ok := o.inflight[digest]
			if !ok {
				continue
			}
			if v.Error != "" {
				span.SetStatus(codes.Error, v.Error)
			}
			span.End(trace.WithTimestamp(*v.Completed))
			delete(o.inflight, digest)
		}
	}
}

// Flush ends any spans that never received a Completed timestamp (error/cancel path).
func (o *Observer) Flush() {
	for digest, span := range o.inflight {
		span.SetStatus(codes.Error, "vertex did not complete")
		span.End()
		delete(o.inflight, digest)
	}
}
