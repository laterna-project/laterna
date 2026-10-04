package rpc

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/metrics"
	"github.com/laterna-project/laterna/internal/telemetry"
)

// Call metrics.

// MetricsPath is the path of the metrics.
const MetricsPath = "/metrics"

// metricsInterceptor counts calls by procedure and code, and measures the duration of unary calls
// (that of a stream is its whole life, which is of no use here). It is the outermost one, so it
// sees the final codes.
type metricsInterceptor struct {
	calls    metrics.Counter
	duration metrics.Histogram
}

func newMetricsInterceptor(reg *metrics.Registry) metricsInterceptor {
	return metricsInterceptor{
		calls:    reg.Counter("laterna_rpc_requests_total", "API calls, by procedure and code.", "procedure", "code"),
		duration: reg.Histogram("laterna_rpc_duration_seconds", "Duration of unary API calls.", metrics.DurationBuckets, "procedure"),
	}
}

func codeLabel(err error) string {
	if err == nil {
		return "ok"
	}
	return connect.CodeOf(err).String()
}

func (m metricsInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		start := time.Now()
		resp, err := next(ctx, req)
		procedure := req.Spec().Procedure
		m.duration.Observe(time.Since(start).Seconds(), procedure)
		m.calls.Inc(procedure, codeLabel(err))
		return resp, err
	}
}

func (m metricsInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		err := next(ctx, conn)
		m.calls.Inc(conn.Spec().Procedure, codeLabel(err))
		return err
	}
}

func (m metricsInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// traceInterceptor names the request's span after the called procedure and records how it went: as
// an error only for a server fault (internal, unknown, unavailable, data loss), not for an item
// that was not found or a rejected argument.
type traceInterceptor struct{}

func annotate(ctx context.Context, procedure string, err error) {
	span := telemetry.SpanFromContext(ctx)
	if span == nil {
		return
	}
	name := strings.TrimPrefix(procedure, "/")
	service, method, _ := strings.Cut(name, "/")
	span.SetName(name)
	span.SetAttributes(telemetry.String("rpc.system", "connect_rpc"), telemetry.String("rpc.service", service), telemetry.String("rpc.method", method))
	if err == nil {
		return
	}
	code := connect.CodeOf(err)
	span.SetAttributes(telemetry.String("rpc.connect_rpc.error_code", code.String()))
	switch code { //nolint:exhaustive // the other codes are expected refusals
	case connect.CodeInternal, connect.CodeUnknown, connect.CodeUnavailable, connect.CodeDataLoss:
		span.Fail(err.Error())
	}
}

func (traceInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		resp, err := next(ctx, req)
		annotate(ctx, req.Spec().Procedure, err)
		return resp, err
	}
}

func (traceInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		err := next(ctx, conn)
		annotate(ctx, conn.Spec().Procedure, err)
		return err
	}
}

func (traceInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// GetMetrics reports whether /metrics is on.
func (s *SystemService) GetMetrics(context.Context, *connect.Request[laternav1.GetMetricsRequest]) (*connect.Response[laternav1.GetMetricsResponse], error) {
	return connect.NewResponse(&laternav1.GetMetricsResponse{Enabled: s.app.MetricsEnabled(), Path: MetricsPath}), nil
}

// EnableMetrics turns /metrics on with a new token.
func (s *SystemService) EnableMetrics(ctx context.Context, _ *connect.Request[laternav1.EnableMetricsRequest]) (*connect.Response[laternav1.EnableMetricsResponse], error) {
	token, err := s.app.EnableMetrics(ctx, principal(ctx))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.EnableMetricsResponse{Token: token, Path: MetricsPath}), nil
}

// DisableMetrics turns /metrics off.
func (s *SystemService) DisableMetrics(ctx context.Context, _ *connect.Request[laternav1.DisableMetricsRequest]) (*connect.Response[laternav1.DisableMetricsResponse], error) {
	if err := s.app.DisableMetrics(ctx, principal(ctx)); err != nil {
		return nil, err
	}
	return connect.NewResponse(&laternav1.DisableMetricsResponse{}), nil
}
