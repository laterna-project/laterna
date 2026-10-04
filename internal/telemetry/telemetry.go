// Package telemetry traces requests and background work in the OpenTelemetry format: spans linked
// through the context, propagated between services with the W3C traceparent header, and exported in
// batches over OTLP/HTTP (JSON) to a collector (Jaeger, Tempo, Grafana Alloy...). It is configured
// with the standard OpenTelemetry environment variables. Without a collector everything is off and
// costs nothing. No dependency: the official SDK pulls in gRPC and some forty modules.
package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SpanKind is the kind of a span (OTLP values).
type SpanKind int

// Span kinds.
const (
	Internal SpanKind = 1
	Server   SpanKind = 2
	Client   SpanKind = 3
)

// Attr is a span attribute: string, integer, boolean or float.
type Attr struct {
	Key   string
	Value any
}

// String builds a string attribute.
func String(k, v string) Attr { return Attr{k, v} }

// Int builds an integer attribute.
func Int(k string, v int) Attr { return Attr{k, int64(v)} }

// Bool builds a boolean attribute.
func Bool(k string, v bool) Attr { return Attr{k, v} }

// Float builds a float attribute.
func Float(k string, v float64) Attr { return Attr{k, v} }

// Tracer creates spans. The zero value (or nil) is disabled.
type Tracer struct {
	exp     *exporter
	sampler sampler
}

// Disabled returns a tracer that does nothing.
func Disabled() *Tracer { return &Tracer{} }

// Enabled reports whether spans are exported.
func (t *Tracer) Enabled() bool { return t != nil && t.exp != nil }

// spanContext identifies a span, possibly a remote one received through traceparent.
type spanContext struct {
	traceID [16]byte
	spanID  [8]byte
	sampled bool
}

// Span is an operation in progress. A nil Span (disabled tracer) accepts every call.
type Span struct {
	tracer *Tracer
	sc     spanContext
	parent [8]byte

	mu      sync.Mutex
	name    string
	renamed bool
	kind    SpanKind
	start   time.Time
	attrs   []Attr
	status  int
	message string
	ended   bool
}

type spanKey struct{}

type remoteKey struct{}

// SpanFromContext returns the current span, or nil.
func SpanFromContext(ctx context.Context) *Span {
	s, _ := ctx.Value(spanKey{}).(*Span)
	return s
}

// ContextWithSpan attaches a span to another context, for work that outlives its request.
func ContextWithSpan(ctx context.Context, s *Span) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, spanKey{}, s)
}

// Start opens a span as a child of the one in the context, or of the remote parent found by
// Extract.
func (t *Tracer) Start(ctx context.Context, name string, kind SpanKind, attrs ...Attr) (context.Context, *Span) {
	if !t.Enabled() {
		return ctx, nil
	}
	s := &Span{tracer: t, name: name, kind: kind, start: time.Now(), attrs: attrs}
	var parent *spanContext
	if p := SpanFromContext(ctx); p != nil {
		parent = &p.sc
	} else if r, ok := ctx.Value(remoteKey{}).(spanContext); ok {
		parent = &r
	}
	if parent != nil {
		s.sc.traceID, s.parent, s.sc.sampled = parent.traceID, parent.spanID, parent.sampled
	} else {
		_, _ = rand.Read(s.sc.traceID[:])
		s.sc.sampled = t.sampler.sample(s.sc.traceID)
	}
	_, _ = rand.Read(s.sc.spanID[:])
	return context.WithValue(ctx, spanKey{}, s), s
}

// SetName renames the span (a route only known at the end, an API procedure).
func (s *Span) SetName(name string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.name, s.renamed = name, true
	s.mu.Unlock()
}

// Renamed reports whether the span was already renamed.
func (s *Span) Renamed() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renamed
}

// SetAttributes adds attributes.
func (s *Span) SetAttributes(attrs ...Attr) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.attrs = append(s.attrs, attrs...)
	s.mu.Unlock()
}

// Fail marks the span as failed.
func (s *Span) Fail(message string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.status, s.message = statusError, message
	s.mu.Unlock()
}

// End finishes the span (once) and hands it to the exporter if it is sampled.
func (s *Span) End() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	d := spanData{
		sc: s.sc, parent: s.parent, name: s.name, kind: s.kind, start: s.start, end: time.Now(),
		attrs: s.attrs, status: s.status, message: s.message,
	}
	s.mu.Unlock()
	if d.sc.sampled {
		s.tracer.exp.add(d)
	}
}

// TraceID returns the trace ID of a sampled span, "" otherwise. Meant for logs.
func (s *Span) TraceID() string {
	if s == nil || !s.sc.sampled {
		return ""
	}
	return hex.EncodeToString(s.sc.traceID[:])
}

// statusError is the OTLP status code of a failed span.
const statusError = 2

// W3C propagation (traceparent).

const traceparentHeader = "Traceparent"

// Extract reads the traceparent header of a request. Spans opened afterwards are its children.
func (t *Tracer) Extract(ctx context.Context, h http.Header) context.Context {
	if !t.Enabled() {
		return ctx
	}
	sc, ok := parseTraceparent(h.Get(traceparentHeader))
	if !ok {
		return ctx
	}
	return context.WithValue(ctx, remoteKey{}, sc)
}

// Inject writes the traceparent header of the current span into an outgoing request.
func Inject(ctx context.Context, h http.Header) {
	s := SpanFromContext(ctx)
	if s == nil {
		return
	}
	flags := "00"
	if s.sc.sampled {
		flags = "01"
	}
	h.Set(traceparentHeader, "00-"+hex.EncodeToString(s.sc.traceID[:])+"-"+hex.EncodeToString(s.sc.spanID[:])+"-"+flags)
}

// parseTraceparent reads "00-<trace, 32 hex>-<span, 16 hex>-<flags, 2 hex>".
func parseTraceparent(v string) (spanContext, bool) {
	parts := strings.Split(strings.TrimSpace(v), "-")
	if len(parts) < 4 || len(parts[0]) != 2 || parts[0] == "ff" || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return spanContext{}, false
	}
	if parts[0] == "00" && len(parts) != 4 {
		return spanContext{}, false
	}
	var sc spanContext
	t, err1 := hex.DecodeString(parts[1])
	s, err2 := hex.DecodeString(parts[2])
	f, err3 := hex.DecodeString(parts[3])
	if err1 != nil || err2 != nil || err3 != nil {
		return spanContext{}, false
	}
	copy(sc.traceID[:], t)
	copy(sc.spanID[:], s)
	if sc.traceID == ([16]byte{}) || sc.spanID == ([8]byte{}) {
		return spanContext{}, false
	}
	sc.sampled = f[0]&1 == 1
	return sc, true
}

// Sampling.

// sampler decides for root traces. The others follow their parent.
type sampler struct {
	// ratio of traces kept (1 keeps all).
	ratio float64
}

// sample does what OpenTelemetry's TraceIdRatioBased does: the last 8 bytes of the ID are compared
// to the wanted ratio.
func (s sampler) sample(id [16]byte) bool {
	switch {
	case s.ratio >= 1:
		return true
	case s.ratio <= 0:
		return false
	}
	bound := uint64(s.ratio * (1 << 63))
	return binary.BigEndian.Uint64(id[8:])>>1 < bound
}

// HTTP client.

// Transport traces outgoing requests (Sonarr, Radarr, images, OpenID Connect) and passes the trace
// on to them.
func (t *Tracer) Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if !t.Enabled() {
		return base
	}
	return &transport{tracer: t, base: base}
}

type transport struct {
	tracer *Tracer
	base   http.RoundTripper
}

func (tr *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if SpanFromContext(req.Context()) == nil {
		return tr.base.RoundTrip(req) // no trace in progress: do not create an orphan span
	}
	ctx, span := tr.tracer.Start(req.Context(), req.Method, Client,
		String("http.request.method", req.Method), String("server.address", req.URL.Hostname()))
	req = req.Clone(ctx)
	Inject(ctx, req.Header)
	resp, err := tr.base.RoundTrip(req)
	switch {
	case err != nil:
		span.Fail(err.Error())
	case resp.StatusCode >= 400:
		span.SetAttributes(Int("http.response.status_code", resp.StatusCode))
		span.Fail(fmt.Sprintf("HTTP %d", resp.StatusCode))
	default:
		span.SetAttributes(Int("http.response.status_code", resp.StatusCode))
	}
	span.End()
	return resp, err
}
