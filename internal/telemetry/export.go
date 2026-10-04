package telemetry

import (
	"bytes"
	"cmp"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Export over OTLP/HTTP, encoded as JSON (OTLP 1.x): batches of up to 512 spans, every 5 s. When
// the queue is full (collector unreachable) the extra spans are dropped and counted.

const (
	batchSize   = 512
	queueSize   = 4096
	flushEvery  = 5 * time.Second
	sendTimeout = 10 * time.Second
	scopeName   = "github.com/laterna-project/laterna"
)

type spanData struct {
	sc      spanContext
	parent  [8]byte
	name    string
	kind    SpanKind
	start   time.Time
	end     time.Time
	attrs   []Attr
	status  int
	message string
}

type exporter struct {
	endpoint string
	headers  map[string]string
	client   *http.Client
	resource []Attr
	version  string
	log      *slog.Logger

	queue   chan spanData
	flushCh chan chan struct{}
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
	dropped atomic.Int64
	// lastWarn is the last time a failed export was logged (once a minute at most).
	lastWarn atomic.Int64
}

func newExporter(ctx context.Context, endpoint string, headers map[string]string, resource []Attr, version string, log *slog.Logger) *exporter {
	e := &exporter{
		endpoint: endpoint, headers: headers, client: &http.Client{Timeout: sendTimeout}, resource: resource, version: version, log: log,
		queue: make(chan spanData, queueSize), flushCh: make(chan chan struct{}), stop: make(chan struct{}), done: make(chan struct{}),
	}
	// Exports are detached from the context that created the tracer: they go on until Shutdown.
	go e.run(context.WithoutCancel(ctx))
	return e
}

func (e *exporter) add(d spanData) {
	select {
	case e.queue <- d:
	default:
		e.dropped.Add(1)
	}
}

func (e *exporter) run(ctx context.Context) {
	defer close(e.done)
	ticker := time.NewTicker(flushEvery)
	defer ticker.Stop()
	var batch []spanData
	send := func() {
		if len(batch) > 0 {
			e.send(ctx, batch)
			batch = nil
		}
	}
	drain := func() {
		for {
			select {
			case d := <-e.queue:
				batch = append(batch, d)
				if len(batch) >= batchSize {
					send()
				}
			default:
				return
			}
		}
	}
	for {
		select {
		case d := <-e.queue:
			batch = append(batch, d)
			if len(batch) >= batchSize {
				send()
			}
		case <-ticker.C:
			send()
		case ack := <-e.flushCh:
			drain()
			send()
			close(ack)
		case <-e.stop:
			drain()
			send()
			return
		}
	}
}

// Flush sends the finished spans right away.
func (t *Tracer) Flush(ctx context.Context) {
	if t == nil || t.exp == nil {
		return
	}
	ack := make(chan struct{})
	select {
	case t.exp.flushCh <- ack:
	case <-t.exp.done:
		return
	case <-ctx.Done():
		return
	}
	select {
	case <-ack:
	case <-ctx.Done():
	}
}

// Shutdown sends the last spans and stops exporting.
func (t *Tracer) Shutdown(ctx context.Context) {
	if t == nil || t.exp == nil {
		return
	}
	t.exp.once.Do(func() { close(t.exp.stop) })
	select {
	case <-t.exp.done:
	case <-ctx.Done():
	}
	if n := t.exp.dropped.Load(); n > 0 {
		t.exp.log.Warn("traces: spans dropped (collector too slow or unreachable)", "count", n)
	}
}

func (e *exporter) send(ctx context.Context, batch []spanData) {
	body, err := json.Marshal(e.encode(batch))
	if err != nil {
		e.warn("traces: cannot encode", err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		e.warn("traces: cannot build request", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range e.headers {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.warn("traces: collector unreachable", err)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		e.warn("traces: export rejected by the collector", fmt.Errorf("HTTP %d", resp.StatusCode))
	}
}

func (e *exporter) warn(msg string, err error) {
	now := time.Now().Unix()
	if last := e.lastWarn.Load(); now-last >= 60 && e.lastWarn.CompareAndSwap(last, now) {
		e.log.Warn(msg, "endpoint", e.endpoint, "err", err)
	}
}

// OTLP JSON.

type otlpRequest struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

type otlpResourceSpans struct {
	Resource   otlpResource     `json:"resource"`
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
}

type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes"`
}

type otlpScopeSpans struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}

type otlpScope struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type otlpSpan struct {
	TraceID      string         `json:"traceId"`
	SpanID       string         `json:"spanId"`
	ParentSpanID string         `json:"parentSpanId,omitempty"`
	Name         string         `json:"name"`
	Kind         SpanKind       `json:"kind"`
	Start        string         `json:"startTimeUnixNano"`
	End          string         `json:"endTimeUnixNano"`
	Attributes   []otlpKeyValue `json:"attributes,omitempty"`
	Status       otlpStatus     `json:"status"`
}

type otlpStatus struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type otlpKeyValue struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

// otlpValue has exactly one value set. 64-bit integers are written as strings, as the Protobuf JSON
// mapping wants.
type otlpValue struct {
	StringValue *string  `json:"stringValue,omitempty"`
	IntValue    *string  `json:"intValue,omitempty"`
	BoolValue   *bool    `json:"boolValue,omitempty"`
	DoubleValue *float64 `json:"doubleValue,omitempty"`
}

func keyValues(attrs []Attr) []otlpKeyValue {
	out := make([]otlpKeyValue, 0, len(attrs))
	for _, a := range attrs {
		var v otlpValue
		switch x := a.Value.(type) {
		case string:
			s := strings.ToValidUTF8(x, "�")
			v.StringValue = &s
		case int64:
			s := strconv.FormatInt(x, 10)
			v.IntValue = &s
		case bool:
			v.BoolValue = &x
		case float64:
			v.DoubleValue = &x
		default:
			s := fmt.Sprint(x)
			v.StringValue = &s
		}
		out = append(out, otlpKeyValue{Key: a.Key, Value: v})
	}
	return out
}

func (e *exporter) encode(batch []spanData) otlpRequest {
	spans := make([]otlpSpan, 0, len(batch))
	for _, d := range batch {
		s := otlpSpan{
			TraceID: hex.EncodeToString(d.sc.traceID[:]), SpanID: hex.EncodeToString(d.sc.spanID[:]),
			Name: d.name, Kind: d.kind, Start: strconv.FormatInt(d.start.UnixNano(), 10), End: strconv.FormatInt(d.end.UnixNano(), 10),
			Attributes: keyValues(d.attrs), Status: otlpStatus{Code: d.status, Message: d.message},
		}
		if d.parent != ([8]byte{}) {
			s.ParentSpanID = hex.EncodeToString(d.parent[:])
		}
		spans = append(spans, s)
	}
	return otlpRequest{ResourceSpans: []otlpResourceSpans{{
		Resource:   otlpResource{Attributes: keyValues(e.resource)},
		ScopeSpans: []otlpScopeSpans{{Scope: otlpScope{Name: scopeName, Version: e.version}, Spans: spans}},
	}}}
}

// Configuration from the environment.

// FromEnv creates the tracer from the standard OpenTelemetry variables:
//   - OTEL_EXPORTER_OTLP_TRACES_ENDPOINT (full URL) or OTEL_EXPORTER_OTLP_ENDPOINT
//     (+ /v1/traces): without them nothing is traced;
//   - OTEL_EXPORTER_OTLP_HEADERS (and _TRACES_HEADERS): "key=value,...";
//   - OTEL_EXPORTER_OTLP_PROTOCOL: only http/json is supported;
//   - OTEL_SERVICE_NAME (laterna by default), OTEL_RESOURCE_ATTRIBUTES;
//   - OTEL_TRACES_SAMPLER (always_on, always_off, traceidratio and their parentbased_* forms)
//     and OTEL_TRACES_SAMPLER_ARG (the ratio kept);
//   - OTEL_SDK_DISABLED=true turns everything off.
func FromEnv(ctx context.Context, getenv func(string) string, version string, log *slog.Logger) (*Tracer, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if strings.EqualFold(strings.TrimSpace(getenv("OTEL_SDK_DISABLED")), "true") {
		return Disabled(), nil
	}
	endpoint := strings.TrimSpace(getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))
	if endpoint == "" {
		if base := strings.TrimSpace(getenv("OTEL_EXPORTER_OTLP_ENDPOINT")); base != "" {
			endpoint = strings.TrimRight(base, "/") + "/v1/traces"
		}
	}
	if endpoint == "" {
		return Disabled(), nil
	}
	if u, err := url.Parse(endpoint); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT: invalid address %q", endpoint)
	}
	if p := strings.TrimSpace(cmp.Or(getenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"), getenv("OTEL_EXPORTER_OTLP_PROTOCOL"))); p != "" && p != "http/json" {
		log.Warn("traces: only the http/json protocol is supported, it will be used", "protocol", p)
	}
	headers, err := keyValueList(getenv("OTEL_EXPORTER_OTLP_HEADERS"))
	if err != nil {
		return nil, fmt.Errorf("OTEL_EXPORTER_OTLP_HEADERS: %w", err)
	}
	traceHeaders, err := keyValueList(getenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS"))
	if err != nil {
		return nil, fmt.Errorf("OTEL_EXPORTER_OTLP_TRACES_HEADERS: %w", err)
	}
	for k, v := range traceHeaders {
		headers[k] = v
	}
	s, err := samplerFromEnv(getenv("OTEL_TRACES_SAMPLER"), getenv("OTEL_TRACES_SAMPLER_ARG"))
	if err != nil {
		return nil, err
	}
	if s.ratio <= 0 {
		return Disabled(), nil
	}
	resAttrs, err := keyValueList(getenv("OTEL_RESOURCE_ATTRIBUTES"))
	if err != nil {
		return nil, fmt.Errorf("OTEL_RESOURCE_ATTRIBUTES: %w", err)
	}
	service := cmp.Or(strings.TrimSpace(getenv("OTEL_SERVICE_NAME")), resAttrs["service.name"], "laterna")
	resource := []Attr{String("service.name", service), String("service.version", version)}
	for k, v := range resAttrs {
		if k != "service.name" && k != "service.version" {
			resource = append(resource, String(k, v))
		}
	}
	log.Info("traces exported", "endpoint", endpoint, "service", service, "sampler_ratio", s.ratio)
	return &Tracer{exp: newExporter(ctx, endpoint, headers, resource, version, log), sampler: s}, nil
}

func samplerFromEnv(name, arg string) (sampler, error) {
	ratio := 1.0
	if a := strings.TrimSpace(arg); a != "" {
		r, err := strconv.ParseFloat(a, 64)
		if err != nil || r < 0 || r > 1 {
			return sampler{}, fmt.Errorf("OTEL_TRACES_SAMPLER_ARG: %q is not a ratio between 0 and 1", a)
		}
		ratio = r
	}
	switch strings.TrimSpace(strings.ToLower(name)) {
	case "", "always_on", "parentbased_always_on":
		return sampler{ratio: 1}, nil
	case "always_off", "parentbased_always_off":
		return sampler{ratio: 0}, nil
	case "traceidratio", "parentbased_traceidratio":
		return sampler{ratio: ratio}, nil
	}
	return sampler{}, fmt.Errorf("OTEL_TRACES_SAMPLER: %q is not supported", name)
}

// keyValueList reads "key=value,key2=value2" (values are URL-encoded).
func keyValueList(v string) (map[string]string, error) {
	out := map[string]string{}
	for item := range strings.SplitSeq(v, ",") {
		if item = strings.TrimSpace(item); item == "" {
			continue
		}
		k, val, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, errors.New("\"key=value\" expected")
		}
		decoded, err := url.PathUnescape(strings.TrimSpace(val))
		if err != nil {
			return nil, err
		}
		out[strings.TrimSpace(k)] = decoded
	}
	return out, nil
}
