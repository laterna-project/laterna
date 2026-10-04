package app

import (
	"context"

	"github.com/laterna-project/laterna/internal/telemetry"
)

// Tracing: each request is a span (API layer), and so is each background job run and each FFmpeg
// run. Outgoing calls are their children.

// Tracer returns the tracer (disabled without a collector).
func (a *App) Tracer() *telemetry.Tracer { return a.tracer }

// traceJob opens the span of a background job run, the root of its own trace.
func (a *App) traceJob(ctx context.Context, kind, target string) (context.Context, func(error)) {
	ctx, span := a.tracer.Start(ctx, "job "+kind, telemetry.Internal, telemetry.String("job.kind", kind), telemetry.String("job.target", target))
	return ctx, func(err error) {
		if err != nil {
			span.Fail(err.Error())
		}
		span.End()
	}
}
